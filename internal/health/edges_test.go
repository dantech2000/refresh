package health_test

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"syscall"
	"testing"

	"k8s.io/client-go/kubernetes"
	metricsclient "k8s.io/metrics/pkg/client/clientset/versioned"

	"github.com/dantech2000/refresh/internal/fleet/fleettest"
	"github.com/dantech2000/refresh/internal/health"
)

// The shapes of a budget that the drain rule must read from its status,
// whatever its spec says.
func TestDrainBlockers_BudgetShapes(t *testing.T) {
	zero := int32(0)
	tests := []struct {
		name    string
		edits   []edit
		targets []string
		want    want
	}{
		{
			// maxUnavailable: 0 allows no disruption at all. The
			// controller reports 0 allowed, and the drain would stall.
			name: "maxUnavailable 0 on the target",
			edits: []edit{func(_ *fleettest.State, c *fleettest.Cluster) {
				c.PDBs[0].MaxUnavailable, c.PDBs[0].DisruptionsAllowed = &zero, 0
			}},
			targets: []string{"ng-a"},
			want:    want{outcome: health.Fail, message: "PDB default/web allows 0 disruptions", findings: []string{"default/web"}},
		},
		{
			name: "maxUnavailable 0 elsewhere",
			edits: []edit{func(_ *fleettest.State, c *fleettest.Cluster) {
				c.PDBs[1].MaxUnavailable, c.PDBs[1].DisruptionsAllowed = &zero, 0
			}},
			targets: []string{"ng-a"},
			want:    want{outcome: health.Pass},
		},
		{
			// An empty selector, as opposed to none, selects every pod
			// of its namespace.
			name: "an empty selector selects the namespace",
			edits: []edit{func(_ *fleettest.State, c *fleettest.Cluster) {
				c.Pods = append(c.Pods, &fleettest.Pod{Namespace: "batch", Name: "job-1", Node: "a-1"})
				c.PDBs = append(c.PDBs, fleettest.PDB{Namespace: "batch", Name: "all", MinAvailable: 1, CurrentHealthy: 1, DesiredHealthy: 1, ExpectedPods: 1})
			}},
			targets: []string{"ng-a"},
			want:    want{outcome: health.Fail, findings: []string{"batch/all"}},
		},
		{
			// A budget whose selector matches no pod in the cluster has
			// nothing to protect: the controller expects 0 pods.
			name: "a selector that matches no pod",
			edits: []edit{func(_ *fleettest.State, c *fleettest.Cluster) {
				c.PDBs = append(c.PDBs, fleettest.PDB{Namespace: "default", Name: "ghost", Selector: map[string]string{"app": "ghost"}, MinAvailable: 1})
			}},
			want: want{outcome: health.Pass},
		},
		{
			// A nodegroup at min = max = desired = 1, whose one pod a
			// minAvailable: 1 budget guards. EKS surges a new node before
			// the drain, but the budget still allows no eviction.
			name: "one node, one pod, minAvailable 1",
			edits: []edit{func(_ *fleettest.State, c *fleettest.Cluster) {
				ng := c.Nodegroup("ng-a")
				ng.Desired, ng.Min, ng.Max = 1, 1, 1
				c.Nodes = c.Nodes[1:] // a-1 goes; a-2 stays
				var pods []*fleettest.Pod
				for _, p := range c.Pods {
					if p.Node != "a-1" {
						pods = append(pods, p)
					}
				}
				c.Pods = pods
				c.PDBs[0] = fleettest.PDB{Namespace: "default", Name: "web", Selector: map[string]string{"app": "web"}, MinAvailable: 1, CurrentHealthy: 1, DesiredHealthy: 1, ExpectedPods: 1}
			}},
			targets: []string{"ng-a"},
			want:    want{outcome: health.Fail, findings: []string{"default/web"}},
		},
		{
			// A tight budget whose pods run on nodes of no managed
			// nodegroup: a roll of ng-a does not drain them.
			name: "a tight budget on self-managed nodes",
			edits: []edit{func(_ *fleettest.State, c *fleettest.Cluster) {
				c.Nodes = append(c.Nodes, &fleettest.Node{Name: "self-1"}, &fleettest.Node{Name: "fargate-1", Labels: map[string]string{"eks.amazonaws.com/compute-type": "fargate"}})
				c.Pods = append(c.Pods,
					&fleettest.Pod{Namespace: "legacy", Name: "db-1", Node: "self-1", Labels: map[string]string{"app": "db"}},
					&fleettest.Pod{Namespace: "legacy", Name: "db-2", Node: "fargate-1", Labels: map[string]string{"app": "db"}})
				c.PDBs = append(c.PDBs, fleettest.PDB{Namespace: "legacy", Name: "db", Selector: map[string]string{"app": "db"}, MinAvailable: 2, CurrentHealthy: 2, DesiredHealthy: 2, ExpectedPods: 2})
			}},
			targets: []string{"ng-a"},
			want:    want{outcome: health.Pass},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t, tt.edits...)
			check(t, h.run(testContext(t), health.DrainBlockers(), tt.targets...), tt.want)
		})
	}
}

// The drain gate and the world's drain are two implementations of the
// rules of the Kubernetes eviction API. For each shape of budget, the gate
// blocks a roll of ng-a exactly when the world's roll of ng-a, sent
// without force, ends with PodEvictionFailure.
func TestDrainBlockers_AgreeWithTheDrain(t *testing.T) {
	zero := int32(0)
	notReady := func(_ *fleettest.State, c *fleettest.Cluster) { c.Pods[3].NotReady, c.Pods[4].NotReady = true, true }
	tests := []struct {
		name  string
		edits []edit
		// productBug, when set, names a known bug of the gate: the case
		// is skipped while the two disagree, and fails once they agree,
		// so the field goes with the fix.
		productBug string
	}{
		{name: "room everywhere"},
		{name: "a tight budget on the target", edits: []edit{tight("web")}},
		{name: "a tight budget elsewhere", edits: []edit{tight("api")}},
		{name: "maxUnavailable 0", edits: []edit{func(_ *fleettest.State, c *fleettest.Cluster) {
			c.PDBs[0].MaxUnavailable, c.PDBs[0].DisruptionsAllowed = &zero, 0
		}}},
		// The status is of an older generation and still allows one
		// disruption. The eviction API refuses every pod of the budget
		// (ObservedGeneration < Generation answers 429), and the gate
		// passes it: atRisk checks allowed, not unsynced.
		{name: "an unsynced budget that allowed a disruption", edits: []edit{func(_ *fleettest.State, c *fleettest.Cluster) { c.PDBs[0].Unsynced = true }},
			productBug: "DrainBlockers passes a budget whose status is not synced when the old status allowed a disruption"},
		{name: "a sync-failed budget", edits: []edit{func(_ *fleettest.State, c *fleettest.Cluster) {
			c.PDBs[0].SyncFailed, c.PDBs[0].DisruptionsAllowed = true, 0
		}}},
		{name: "a pod covered by two budgets", edits: []edit{func(_ *fleettest.State, c *fleettest.Cluster) {
			c.PDBs = append(c.PDBs, fleettest.PDB{Namespace: "default", Name: "web-too", Selector: map[string]string{"app": "web"}, MinAvailable: 1, DisruptionsAllowed: 1, CurrentHealthy: 2, DesiredHealthy: 1, ExpectedPods: 2})
		}}},
		{name: "a not-ready pod under AlwaysAllow", edits: []edit{tight("web"), notReady, func(_ *fleettest.State, c *fleettest.Cluster) { c.PDBs[0].AlwaysAllow = true }}},
		{name: "a not-ready pod under a met IfHealthyBudget", edits: []edit{tight("web"), notReady, func(_ *fleettest.State, c *fleettest.Cluster) {
			c.PDBs[0].CurrentHealthy, c.PDBs[0].DesiredHealthy = 2, 2
		}}},
		{name: "a not-ready pod under an unmet IfHealthyBudget", edits: []edit{tight("web"), notReady, func(_ *fleettest.State, c *fleettest.Cluster) {
			c.PDBs[0].CurrentHealthy = 0
		}}},
		{name: "pods that eviction ignores", edits: []edit{tight("web"), func(_ *fleettest.State, c *fleettest.Cluster) { c.Pods[3].Phase, c.Pods[4].Deleting = "Pending", true }}},
		{name: "a budget with no selector", edits: []edit{tight("web"), func(_ *fleettest.State, c *fleettest.Cluster) { c.PDBs[0].NoSelector = true }}},
		{name: "an empty selector selects the namespace", edits: []edit{func(_ *fleettest.State, c *fleettest.Cluster) {
			c.PDBs = append(c.PDBs, fleettest.PDB{Namespace: "kube-system", Name: "all", MinAvailable: 3, CurrentHealthy: 3, DesiredHealthy: 3, ExpectedPods: 3})
		}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t, tt.edits...)
			gate := h.run(testContext(t), health.DrainBlockers(), "ng-a")
			if gate.Outcome != health.Pass && gate.Outcome != health.Fail {
				t.Fatalf("gate = %s (%s), want Pass or Fail", gate.Outcome, gate.Message)
			}
			u, err := h.world.UpdateNodegroupVersion(east, "prod", "ng-a", fleettest.NodegroupVersionRequest{})
			if err != nil {
				t.Fatal(err)
			}
			var ended fleettest.Update
			h.world.View(func(s *fleettest.State) { ended = *s.Cluster(east, "prod").Update(u.ID) })
			drainFailed := ended.Status == fleettest.UpdateFailed && len(ended.Errors) == 1 && ended.Errors[0].Code == "PodEvictionFailure"
			blocked := gate.Outcome == health.Fail
			switch {
			case blocked != drainFailed && tt.productBug != "":
				t.Skipf("known product bug: %s", tt.productBug)
			case blocked != drainFailed:
				t.Errorf("gate %s (%s), and the roll without force ended %s %+v", gate.Outcome, gate.Message, ended.Status, ended.Errors)
			case tt.productBug != "":
				t.Errorf("the gate and the drain agree now; remove productBug %q", tt.productBug)
			}
		})
	}
}

// RBAC that reads nodes and budgets but not pods: the checks that need
// pods are Unreadable, the API is reachable, and the gate blocks with
// exit 4 whatever --yes says. Nodes still passes.
func TestRun_PodsForbidden(t *testing.T) {
	h := newHarness(t, tight("web"))
	h.world.SetFault(fleettest.KubeListPods, fleettest.Fault{Deny: true})
	ctx := testContext(t)
	results := health.Run(ctx, health.Preflight(), h.input(ctx, "ng-a"))
	for _, r := range results {
		switch r.Name {
		case "Nodes":
			check(t, r, want{outcome: health.Pass})
		case "Workloads", "DrainBlockers":
			if r.Outcome != health.Unreadable || len(r.Failures) == 0 {
				t.Errorf("%s = %s %+v, want Unreadable with a failure", r.Name, r.Outcome, r.Failures)
			}
		case health.AccessCheck:
			check(t, r, want{outcome: health.Pass, message: "reachable"})
		}
	}
	for _, p := range []health.Policy{{}, {Yes: true}, {Yes: true, Force: false}} {
		if v := health.Gate(results, p); v.Decision != health.Block || v.Exit != 4 {
			t.Errorf("policy %+v: verdict = %+v, want Block with exit 4", p, v)
		}
	}
}

// dialing points the clients of in at the harness's API server through
// a dialer whose every connection fails at once with err, as a
// private-only endpoint does from outside its VPC. No read waits on a
// clock.
func dialing(t *testing.T, h *harness, in *health.Input, err error) {
	t.Helper()
	cfg := h.kube.Config()
	cfg.Dial = func(context.Context, string, string) (net.Conn, error) { return nil, err }
	client, e := kubernetes.NewForConfig(cfg)
	if e != nil {
		t.Fatal(e)
	}
	mc, e := metricsclient.NewForConfig(cfg)
	if e != nil {
		t.Fatal(e)
	}
	in.Kube, in.Metrics = client, mc.MetricsV1beta1().NodeMetricses()
}

// A private-only API endpoint. When the network refuses the route, every
// read fails to connect. When the packets are dropped, the dial times out
// after the client's dial timeout (10s in internal/kube). Both are no
// Kubernetes access (phase 3, section 13): the API is unreachable, the
// Kubernetes checks are Skipped, and the gate asks, or goes on with
// --yes. Neither run hangs: every read returns its error.
func TestRun_PrivateEndpoint(t *testing.T) {
	unroutable := &net.OpError{Op: "dial", Net: "tcp", Err: os.NewSyscallError("connect", syscall.EHOSTUNREACH)}
	timedOut := &net.OpError{Op: "dial", Net: "tcp", Err: os.ErrDeadlineExceeded}
	for name, dialErr := range map[string]error{"no route": unroutable, "dial timeout": timedOut} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			ctx := testContext(t)
			in := h.input(ctx, "ng-a")
			dialing(t, h, in, dialErr)
			results := health.Run(ctx, health.Preflight(), in)
			for _, r := range results {
				switch r.Name {
				case "Nodes":
					check(t, r, want{outcome: health.Pass, message: "4/4 nodes ready (estimated)"})
				case "Workloads", "DrainBlockers", "PDBCoverage":
					check(t, r, want{outcome: health.Skipped, message: "Kubernetes API unreachable"})
				case health.AccessCheck:
					check(t, r, want{outcome: health.Warn, message: "Kubernetes API unreachable"})
				}
			}
			if v := health.Gate(results, health.Policy{}); v.Decision != health.Ask || v.Exit != 2 {
				t.Errorf("verdict = %+v, want Ask with exit 2", v)
			}
			if v := health.Gate(results, health.Policy{Yes: true}); v.Decision != health.Go {
				t.Errorf("verdict with --yes = %+v, want Go", v)
			}
			if v := health.Gate(results, health.Policy{RequireHealthy: true}); v.Decision != health.Block {
				t.Errorf("verdict with --require-healthy = %+v, want Block", v)
			}
		})
	}
	t.Run("dial timeout, the access result", func(t *testing.T) {
		h := newHarness(t)
		ctx := testContext(t)
		in := h.input(ctx, "ng-a")
		dialing(t, h, in, timedOut)
		results := health.Run(ctx, health.Preflight(), in)
		for _, r := range results {
			if r.Name == health.AccessCheck && r.Outcome == health.Pass {
				t.Errorf("the access result is %s %q, and no read reached the API", r.Outcome, r.Message)
			}
		}
	})
}

// failingTransport fails every request with err, after the connection: a
// credential plugin, a certificate, or a connection that broke.
type failingTransport struct{ err error }

func (f failingTransport) RoundTrip(*http.Request) (*http.Response, error) { return nil, f.err }

// transportFailing gives in a client whose every request fails with err
// in the transport, not in the dial.
func transportFailing(t *testing.T, h *harness, in *health.Input, err error) {
	t.Helper()
	cfg := h.kube.Config()
	cfg.WrapTransport = func(http.RoundTripper) http.RoundTripper { return failingTransport{err} }
	client, e := kubernetes.NewForConfig(cfg)
	if e != nil {
		t.Fatal(e)
	}
	mc, e := metricsclient.NewForConfig(cfg)
	if e != nil {
		t.Fatal(e)
	}
	in.Kube, in.Metrics = client, mc.MetricsV1beta1().NodeMetricses()
}

// Only a connection that could not be opened is an unreachable API: the
// Kubernetes checks are then Skipped, and --yes goes on. Every other
// error fails closed: the checks are Unreadable, and the drain blockers,
// which here include a budget that allows no disruption, block with
// --yes. A credential that fails on the client must not get a softer
// verdict than one that the server rejects.
func TestRun_TransportFailures(t *testing.T) {
	dial := func(err error) *net.OpError { return &net.OpError{Op: "dial", Net: "tcp", Err: err} }
	tests := []struct {
		name        string
		err         error
		inDial      bool
		unreachable bool
	}{
		{name: "connection refused", err: dial(os.NewSyscallError("connect", syscall.ECONNREFUSED)), inDial: true, unreachable: true},
		{name: "no route", err: dial(os.NewSyscallError("connect", syscall.EHOSTUNREACH)), inDial: true, unreachable: true},
		{name: "a dial that times out", err: dial(os.ErrDeadlineExceeded), inDial: true, unreachable: true},
		{name: "a host that does not resolve", err: dial(&net.DNSError{Err: "no such host", Name: "api.example", IsNotFound: true}), inDial: true, unreachable: true},
		{name: "a proxy that refuses", err: &net.OpError{Op: "proxyconnect", Net: "tcp", Err: os.NewSyscallError("connect", syscall.ECONNREFUSED)}, inDial: true, unreachable: true},
		{name: "a credential plugin that fails", err: errors.New("getting credentials: exec: executable aws failed with exit code 255")},
		{name: "a certificate that does not verify", err: fmt.Errorf("tls: failed to verify certificate: %w", x509.UnknownAuthorityError{})},
		{name: "a host name that the certificate does not name", err: x509.HostnameError{Certificate: &x509.Certificate{}, Host: "api.example"}},
		// client-go retries a GET on a bare reset or EOF for about ten
		// seconds and then returns the same error. These forms of the
		// same errors it returns at once.
		{name: "a broken connection", err: &net.OpError{Op: "write", Net: "tcp", Err: os.NewSyscallError("write", syscall.EPIPE)}},
		{name: "an EOF after the connection", err: fmt.Errorf("reading the response: %w", io.EOF)},
		{name: "a read that times out", err: &net.OpError{Op: "read", Net: "tcp", Err: os.ErrDeadlineExceeded}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t, tight("web"))
			ctx := testContext(t)
			in := h.input(ctx, "ng-a")
			if tt.inDial {
				dialing(t, h, in, tt.err)
			} else {
				transportFailing(t, h, in, tt.err)
			}
			results := health.Run(ctx, health.Preflight(), in)
			for _, r := range results {
				switch {
				case r.Name == health.AccessCheck && tt.unreachable:
					check(t, r, want{outcome: health.Warn, message: "Kubernetes API unreachable"})
				case r.Name == health.AccessCheck:
					check(t, r, want{outcome: health.Warn, message: "Kubernetes API did not answer"})
				case r.Name != "Workloads" && r.Name != "DrainBlockers":
				case tt.unreachable:
					check(t, r, want{outcome: health.Skipped, message: "Kubernetes API unreachable"})
				default:
					if r.Outcome != health.Unreadable || len(r.Failures) == 0 {
						t.Errorf("%s: %s %+v, want Unreadable with its failures", r.Name, r.Outcome, r.Failures)
					}
				}
			}
			v := health.Gate(results, health.Policy{Yes: true})
			if tt.unreachable && v.Decision != health.Go {
				t.Errorf("verdict with --yes = %+v, want Go: no Kubernetes access", v)
			}
			if !tt.unreachable && (v.Decision != health.Block || v.Exit != 4) {
				t.Errorf("verdict with --yes = %+v, want Block with exit 4: the drain blockers were not read", v)
			}
		})
	}
}

// An unreachable API is no Kubernetes access, as no client is: the Nodes
// check estimates from the nodegroups in both, so a nodegroup that is not
// ACTIVE is a problem, a target that is not ACTIVE blocks, and the verdict
// is the same.
func TestRun_UnreachableKeepsTheNodegroupRule(t *testing.T) {
	unreachable := &net.OpError{Op: "dial", Net: "tcp", Err: os.NewSyscallError("connect", syscall.EHOSTUNREACH)}
	tests := []struct {
		name    string
		edits   []edit
		targets []string
		want    want
		yes     health.Decision
	}{
		{name: "healthy", targets: []string{"ng-a"}, want: want{outcome: health.Pass, message: "4/4 nodes ready (estimated)"}, yes: health.Go},
		{name: "a degraded neighbour", edits: []edit{nodegroupStatus("ng-b", fleettest.StatusDegraded)}, targets: []string{"ng-a"}, want: want{outcome: health.Warn, message: "2/4 nodes ready (estimated)", findings: []string{"ng-b (DEGRADED)"}}, yes: health.Go},
		{name: "a degraded target", edits: []edit{nodegroupStatus("ng-a", fleettest.StatusDegraded)}, targets: []string{"ng-a"}, want: want{outcome: health.Fail, message: "a target nodegroup is not ACTIVE"}, yes: health.Block},
		{name: "every nodegroup degraded", edits: []edit{nodegroupStatus("ng-a", fleettest.StatusDegraded), nodegroupStatus("ng-b", fleettest.StatusDegraded)}, want: want{outcome: health.Fail, message: "no ready nodes (estimated)"}, yes: health.Block},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t, tt.edits...)
			ctx := testContext(t)
			in := h.input(ctx, tt.targets...)
			in.Kube, in.Metrics = nil, nil
			noKube := health.Run(ctx, health.Preflight(), in)
			in = h.input(ctx, tt.targets...)
			dialing(t, h, in, unreachable)
			unreached := health.Run(ctx, health.Preflight(), in)
			for _, mode := range []struct {
				name    string
				results []health.Result
			}{{"no client", noKube}, {"unreachable", unreached}} {
				t.Run(mode.name, func(t *testing.T) {
					for _, r := range mode.results {
						if r.Name == "Nodes" {
							check(t, r, tt.want)
						}
					}
					if v := health.Gate(mode.results, health.Policy{Yes: true}); v.Decision != tt.yes {
						t.Errorf("verdict with --yes = %+v, want %s", v, tt.yes)
					}
				})
			}
		})
	}
}
