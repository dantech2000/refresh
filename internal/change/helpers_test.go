package change

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/retry"

	"github.com/dantech2000/refresh/internal/awsx"
	"github.com/dantech2000/refresh/internal/awsx/awsxtest"
	"github.com/dantech2000/refresh/internal/fleet"
	"github.com/dantech2000/refresh/internal/fleet/fleettest"
)

const east = "us-east-1"

// The versions of the test world: vpc-cni and coredns are one version
// behind, and kube-proxy is at the latest. cniNext is a version that only
// Kubernetes 1.34 can run.
const (
	cniOld, cniNew, cniNext = "v1.19.0-eksbuild.1", "v1.20.0-eksbuild.1", "v1.21.0-eksbuild.1"
	dnsOld, dnsNew          = "v1.11.3-eksbuild.2", "v1.11.4-eksbuild.2"
	proxyNow                = "v1.33.0-eksbuild.2"
)

// newWorld returns a world with the cluster prod on Kubernetes 1.33, one
// nodegroup, and three add-ons. edit changes the cluster.
func newWorld(edit func(c *fleettest.Cluster)) *fleettest.World {
	world := fleettest.New()
	world.Edit(func(s *fleettest.State) {
		prod := &fleettest.Cluster{
			Name: "prod", Region: east, Version: "1.33",
			Nodegroups: []*fleettest.Nodegroup{{Name: "web", Version: "1.33"}},
			Addons: []*fleettest.Addon{
				{Name: "coredns", Version: dnsOld},
				{Name: "kube-proxy", Version: proxyNow},
				{Name: "vpc-cni", Version: cniOld},
			},
		}
		if edit != nil {
			edit(prod)
		}
		s.Clusters = append(s.Clusters, prod)
		s.AddonVersions = []fleettest.AddonVersion{
			{Addon: "vpc-cni", Version: cniNext, ClusterVersions: []string{"1.34"}},
			{Addon: "vpc-cni", Version: cniNew, ClusterVersions: []string{"1.33", "1.34"}},
			{Addon: "vpc-cni", Version: cniOld, ClusterVersions: []string{"1.32", "1.33"}},
			{Addon: "coredns", Version: dnsNew, ClusterVersions: []string{"1.33"}},
			{Addon: "coredns", Version: dnsOld, ClusterVersions: []string{"1.32", "1.33"}},
			{Addon: "kube-proxy", Version: proxyNow, ClusterVersions: []string{"1.33"}},
		}
	})
	return world
}

// env is a fake AWS for a world, and the dependencies of a change that
// reach it. The clock of the change is the clock of the world: a pause
// moves the world forward and ends at once, so a test waits for nothing.
type env struct {
	t     *testing.T
	world *fleettest.World
	srv   *awsxtest.Server
	deps  Deps
}

func newEnv(t *testing.T, world *fleettest.World) *env {
	t.Helper()
	srv := awsxtest.NewServer(world)
	t.Cleanup(srv.Close)
	return &env{t: t, world: world, srv: srv, deps: depsFor(world, srv.Config(east))}
}

// depsFor returns the dependencies of a change that reads cfg, with the
// clock of the world.
func depsFor(world *fleettest.World, cfg aws.Config) Deps {
	provider := awsx.NewProvider(cfg, func(o *awsx.ProviderOptions) {
		o.Backoff = retry.BackoffDelayerFunc(func(int, error) (time.Duration, error) { return 0, nil })
	})
	return Deps{
		AWS:   provider,
		Fetch: &fleet.Fetcher{Now: world.Now},
		Now:   world.Now,
		After: func(d time.Duration) <-chan time.Time {
			world.Advance(d)
			done := make(chan time.Time, 1)
			done <- world.Now()
			return done
		},
		AfterFunc: heldTimer,
	}
}

// heldTimer is the timer of a budget in a test whose calls do not hang:
// it never fires. The clock of the test decides when a budget runs out.
func heldTimer(time.Duration, func()) (stop func() bool) {
	return func() bool { return true }
}

// Test kinds for the rules about a change whose phases are a sequence.
// No such change is built yet, so the package has no constant for one.
const (
	testUpgrade  Kind = "ClusterUpgrade"
	testRollback Kind = "ClusterRollback"
)

// intercept puts a server in front of the fake that answers the requests
// that match with an empty JSON object, and passes the others on. It is
// how a test gets an answer that EKS can give and the fake does not: one
// with no update in it. It returns dependencies that reach that server.
func (e *env) intercept(t *testing.T, match func(r *http.Request) bool) Deps {
	t.Helper()
	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if match(r) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte("{}"))
			return
		}
		e.srv.ServeHTTP(w, r)
	}))
	t.Cleanup(front.Close)
	cfg := e.srv.Config(east)
	cfg.BaseEndpoint = aws.String(front.URL)
	return depsFor(e.world, cfg)
}

// operations returns the operations of the requests from index from on.
func (e *env) operations(from int) []string {
	var out []string
	for _, r := range e.srv.Requests()[from:] {
		out = append(out, string(r.Operation))
	}
	return out
}

// count returns how many requests for op the fake got.
func (e *env) count(op fleettest.Operation) int {
	n := 0
	for _, r := range e.srv.Requests() {
		if r.Operation == op {
			n++
		}
	}
	return n
}

// onRequest calls f with the number of each request for op, from 1. The
// request waits until f returns.
func (e *env) onRequest(op fleettest.Operation, f func(n int)) {
	var n atomic.Int32
	e.srv.OnRequest = func(_ context.Context, r awsxtest.Request) {
		if r.Operation == op {
			f(int(n.Add(1)))
		}
	}
}

// startElsewhere starts an update of an add-on in the world, as another
// process would, with the script.
func startElsewhere(t *testing.T, w *fleettest.World, name, version string, script fleettest.UpdateScript) {
	t.Helper()
	w.Edit(func(s *fleettest.State) {
		a := s.Cluster(east, "prod").Addon(name)
		a.UpdateScripts = append([]fleettest.UpdateScript{script}, a.UpdateScripts...)
	})
	if _, err := w.UpdateAddon(east, "prod", name, fleettest.AddonRequest{Version: &version}); err != nil {
		t.Fatalf("starting the update of %s: %v", name, err)
	}
}

// testContext bounds every wait of a test, so a missing handshake fails
// the test and does not hang it.
func testContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	t.Cleanup(cancel)
	return ctx
}
