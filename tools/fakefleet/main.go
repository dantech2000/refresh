// Command fakefleet serves a fake EKS fleet for trying refresh with no AWS
// account: many clusters over many regions, each with a Kubernetes API of
// its own, slow and throttled calls, a region closed to the credentials,
// and a mix of scripted situations (a PDB that blocks a drain, a roll that
// gets stuck, a degraded add-on, ...) that the tool names on stderr. It
// prints the environment that points refresh at it, then serves until
// interrupted.
//
//	go run ./tools/fakefleet -clusters 60 -regions 8 > fleet.env
//	source fleet.env && refresh status -A
//
// Or in one step, with the binary built: task run:sim -- status -A, and
// task run:tui:sim for the UI.
//
// With -world it serves the fleet in a JSON file instead of a generated
// one. The file holds a fleettest.State.
//
// The fleet's clock starts at the wall clock and follows it, -speed times
// as fast, so an update that a script gives a duration ends on its own: a
// roll in -roll, an add-on update in -addon, a control-plane upgrade in
// -upgrade. -speed 0 stops the clock.
package main

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	"github.com/dantech2000/refresh/internal/awsx/awsxtest"
	"github.com/dantech2000/refresh/internal/fleet/fleettest"
	"github.com/dantech2000/refresh/internal/kube/kubetest"
)

func main() { os.Exit(exitCode()) }

// exitCode runs the command and returns its exit code, so the deferred
// cleanup runs before the process exits.
func exitCode() int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdout, os.Stderr); err != nil {
		if !errors.Is(err, flag.ErrHelp) {
			fmt.Fprintln(os.Stderr, "fakefleet:", err)
		}
		return 1
	}
	return 0
}

// allRegions are the regions a generated fleet can use, in order.
var allRegions = []string{"us-east-1", "us-west-2", "eu-west-1", "eu-central-1", "ap-southeast-2", "ap-northeast-1", "ca-central-1", "sa-east-1", "eu-north-1", "ap-south-1"}

// options are the command's flags.
type options struct {
	clusters     int
	regions      int
	latency      time.Duration
	slowRegion   string
	closedRegion string
	throttle     float64
	seed         uint64
	dir          string
	worldFile    string
	addr         string
	speed        float64
	kube         bool
	situations   string
	d            durations

	// mix is situations, parsed.
	mix []situation
	// now is the start of the fleet's clock. Zero means the wall clock.
	now time.Time
}

// tick is how often the fleet's clock catches up with the wall clock.
const tick = 100 * time.Millisecond

// maxLatency keeps ten times the latency, which the slow region uses,
// inside a time.Duration. maxSpeed keeps one tick of the fleet's clock
// inside one too.
const (
	maxLatency = time.Hour
	maxSpeed   = 1e6
)

func parseFlags(args []string, stderr io.Writer) (options, error) {
	var o options
	fs := flag.NewFlagSet("fakefleet", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.IntVar(&o.clusters, "clusters", 60, "clusters in the generated fleet")
	fs.IntVar(&o.regions, "regions", 8, "regions the generated clusters spread over")
	fs.DurationVar(&o.latency, "latency", 250*time.Millisecond, "the most a call waits, in real time (each call waits a random part of it)")
	fs.StringVar(&o.slowRegion, "slow-region", "ap-southeast-2", "a region whose calls take up to 10x -latency (\"\" for none)")
	fs.StringVar(&o.closedRegion, "closed-region", "me-central-1", "a region closed to the credentials (\"\" for none)")
	fs.Float64Var(&o.throttle, "throttle", 0.03, "the share of EKS calls answered with a throttling error")
	fs.Uint64Var(&o.seed, "seed", 1, "the random seed for the fleet and the delays")
	fs.StringVar(&o.dir, "dir", "", "where to write the empty AWS config and the kubeconfig (default: a temp dir)")
	fs.StringVar(&o.worldFile, "world", "", "a JSON file with the fleet to serve, in place of a generated one")
	fs.StringVar(&o.addr, "addr", "127.0.0.1:0", "the address to listen on")
	fs.Float64Var(&o.speed, "speed", 1, "how fast the fleet's clock runs against the wall clock (0 stops it)")
	fs.BoolVar(&o.kube, "kube", true, "serve a Kubernetes API for each cluster and write a kubeconfig (-kube=false for a fleet with no Kubernetes)")
	fs.StringVar(&o.situations, "situations", "all", "the scripted situations to place on the first clusters: all, none, or names separated by commas ("+strings.Join(situationNames(), ", ")+")")
	fs.DurationVar(&o.d.roll, "roll", 2*time.Minute, "how long a nodegroup roll takes on the fleet's clock")
	fs.DurationVar(&o.d.addon, "addon", 30*time.Second, "how long an add-on update takes on the fleet's clock")
	fs.DurationVar(&o.d.upgrade, "upgrade", 3*time.Minute, "how long a control-plane upgrade takes on the fleet's clock")
	if err := fs.Parse(args); err != nil {
		return o, err
	}
	// The comparisons are written so that NaN fails them.
	switch {
	case fs.NArg() > 0:
		return o, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	case o.clusters < 0:
		return o, errors.New("-clusters must not be negative")
	case o.regions < 1 || o.regions > len(allRegions):
		return o, fmt.Errorf("-regions must be 1..%d", len(allRegions))
	case !(o.throttle >= 0 && o.throttle <= 1):
		return o, errors.New("-throttle must be 0..1")
	case o.latency < 0 || o.latency > maxLatency:
		return o, fmt.Errorf("-latency must be 0..%s", maxLatency)
	case !(o.speed >= 0 && o.speed <= maxSpeed):
		return o, fmt.Errorf("-speed must be 0..%g", float64(maxSpeed))
	case o.d.roll <= 0 || o.d.addon <= 0 || o.d.upgrade <= 0:
		return o, errors.New("-roll, -addon, and -upgrade must be above 0")
	}
	var err error
	if o.mix, err = parseSituations(o.situations); err != nil {
		return o, err
	}
	return o, nil
}

// run serves the fleet until ctx ends. It writes the environment to stdout
// once the server listens.
func run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	o, err := parseFlags(args, stderr)
	if err != nil {
		return err
	}
	world, regions, placements, err := newWorld(o)
	if err != nil {
		return err
	}
	handler := newHandler(world, o)
	defer handler.Close()

	dir := o.dir
	if dir == "" {
		if dir, err = os.MkdirTemp("", "fakefleet"); err != nil {
			return err
		}
		defer func() { _ = os.RemoveAll(dir) }()
	} else if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	empty := filepath.Join(dir, "empty")
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		return err
	}
	kubeconfig := filepath.Join(dir, "no-kubeconfig")
	if o.kube {
		servers := serveKube(world, regions[0])
		defer servers.Close()
		kubeconfig = filepath.Join(dir, "kubeconfig")
		if err := servers.writeKubeconfig(kubeconfig); err != nil {
			return err
		}
	}

	var lc net.ListenConfig
	listener, err := lc.Listen(ctx, "tcp", o.addr)
	if err != nil {
		return err
	}
	url := "http://" + listener.Addr().String()
	// A closed region that the fleet does not use is still worth a sweep:
	// named in REFRESH_EKS_REGIONS it fails the sweep (exit 4), and with
	// the variable unset a default sweep skips it.
	swept := regions
	if o.closedRegion != "" && !slices.Contains(regions, o.closedRegion) {
		swept = append(slices.Clone(regions), o.closedRegion)
	}
	for _, kv := range environment(url, dir, empty, kubeconfig, swept) {
		if _, err := fmt.Fprintf(stdout, "export %s=%q\n", kv[0], kv[1]); err != nil {
			_ = listener.Close()
			return err
		}
	}
	_, _ = fmt.Fprintf(stderr, "fakefleet: serving %s in %d regions (slow %s, closed %s, %.0f%% throttled, clock x%g, roll %s, add-on %s, upgrade %s); Ctrl+C to stop\n",
		url, len(regions), orNone(o.slowRegion), orNone(o.closedRegion), o.throttle*100, o.speed, o.d.roll, o.d.addon, o.d.upgrade)
	describe(stderr, placements, o)

	// The clock stops before run returns: stopClock ends the goroutine and
	// waits for it.
	clockCtx, cancelClock := context.WithCancel(ctx)
	clockDone := make(chan struct{})
	go func() {
		defer close(clockDone)
		ticker := time.NewTicker(tick)
		defer ticker.Stop()
		followClock(clockCtx, world, o.speed, time.Now(), ticker.C)
	}()
	stopClock := func() {
		cancelClock()
		<-clockDone
	}
	defer stopClock()

	server := &http.Server{Handler: handler, ReadHeaderTimeout: 10 * time.Second}
	served := make(chan error, 1)
	go func() { served <- server.Serve(listener) }()
	select {
	case err := <-served:
		return err
	case <-ctx.Done():
	}
	handler.Close() // release held requests so Shutdown does not wait for them
	shutdown, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	err = server.Shutdown(shutdown)
	if err != nil {
		// Requests are still open after the grace period. Cut them off.
		_ = server.Close()
	}
	<-served
	return err
}

// describe tells the tester which cluster shows which situation, and what
// the flags of the fleet add.
func describe(stderr io.Writer, placements []placement, o options) {
	if len(placements) == 0 && len(o.mix) == 0 {
		return
	}
	_, _ = fmt.Fprintln(stderr, "fakefleet: situations:")
	for _, p := range placements {
		_, _ = fmt.Fprintf(stderr, "  %-24s %-16s %-20s %s\n", p.cluster, p.region, p.situation.name, p.situation.what)
	}
	if n := len(o.mix) - len(placements); n > 0 {
		_, _ = fmt.Fprintf(stderr, "  (%d situations not placed: the fleet has only %d clusters)\n", n, len(placements))
	}
	if !o.kube {
		_, _ = fmt.Fprintln(stderr, "  (no Kubernetes: the situations that need an API server show as no Kubernetes access)")
	}
}

// advance moves the world's clock forward by a number of nanoseconds that
// may be more than a time.Duration holds, as after a long gap between ticks
// at a high speed. It moves in steps that fit.
func advance(w *fleettest.World, nanoseconds float64) {
	const step = float64(1 << 62)
	for ; nanoseconds >= step; nanoseconds -= step {
		w.Advance(time.Duration(1 << 62))
	}
	w.Advance(time.Duration(nanoseconds))
}

// followClock advances the world's clock by speed times the wall time that
// passes between ticks, until ctx ends. Each tick carries the wall time it
// fired at; start is the wall time the world's clock is in step with.
func followClock(ctx context.Context, w *fleettest.World, speed float64, start time.Time, ticks <-chan time.Time) {
	last := start
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticks:
			if elapsed := now.Sub(last); elapsed > 0 {
				advance(w, float64(elapsed)*speed)
			}
			last = now
		}
	}
}

// environment is what a refresh binary needs to reach the fleet and
// nothing else: no real credentials or profile, and the kubeconfig of the
// fleet's own API servers. regions are the regions a sweep reads; the
// first is the home region.
func environment(url, dir, emptyFile, kubeconfig string, regions []string) [][2]string {
	return [][2]string{
		{"AWS_ENDPOINT_URL", url},
		{"AWS_ACCESS_KEY_ID", "AKIDFAKEFAKEFAKE"},
		{"AWS_SECRET_ACCESS_KEY", "fake-secret"},
		{"AWS_SESSION_TOKEN", ""},
		{"AWS_REGION", regions[0]},
		{"AWS_CONFIG_FILE", emptyFile},
		{"AWS_SHARED_CREDENTIALS_FILE", emptyFile},
		{"AWS_EC2_METADATA_DISABLED", "true"},
		{"AWS_PROFILE", ""},
		{"KUBECONFIG", kubeconfig},
		{"REFRESH_CONFIG_HOME", dir},
		{"REFRESH_EKS_REGIONS", strings.Join(regions, ",")},
	}
}

func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}

// newWorld returns the fleet to serve, the regions it uses, and where its
// situations are: the fleet in the -world file, or a generated one. The
// closed region rejects the credentials for every service.
func newWorld(o options) (*fleettest.World, []string, []placement, error) {
	var (
		state      fleettest.State
		placements []placement
	)
	now := o.now
	if now.IsZero() {
		now = time.Now()
	}
	regions := allRegions[:o.regions]
	if o.worldFile != "" {
		data, err := os.ReadFile(o.worldFile)
		if err != nil {
			return nil, nil, nil, err
		}
		dec := json.NewDecoder(bytes.NewReader(data))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&state); err != nil {
			return nil, nil, nil, fmt.Errorf("reading %s: %w", o.worldFile, err)
		}
		if regions = clusterRegions(&state); len(regions) == 0 {
			regions = allRegions[:1]
		}
	} else {
		rng := rand.New(rand.NewPCG(o.seed, o.seed^0x9e3779b97f4a7c15)) //nolint:gosec // a repeatable fake fleet, not a secret
		state, placements = generate(rng, o.clusters, regions, now, o.d, o.mix, o.kube)
	}
	w := fleettest.NewAt(now)
	w.Edit(func(s *fleettest.State) { *s = state })
	if o.closedRegion != "" {
		w.SetScopedFault(fleettest.Scope{Region: o.closedRegion}, fleettest.Fault{Unauthenticated: true})
	}
	for _, p := range placements {
		if p.situation.start != nil {
			if err := p.situation.start(w, p.region, p.cluster, o.d); err != nil {
				return nil, nil, nil, fmt.Errorf("starting the situation %s on %s: %w", p.situation.name, p.cluster, err)
			}
		}
	}
	return w, regions, placements, nil
}

// clusterRegions returns the regions the clusters of s name, in order of
// first use.
func clusterRegions(s *fleettest.State) []string {
	var out []string
	seen := map[string]bool{}
	for _, c := range s.Clusters {
		if c.Region != "" && !seen[c.Region] {
			seen[c.Region] = true
			out = append(out, c.Region)
		}
	}
	return out
}

// newHandler returns the fake AWS endpoint for w, with the real-time delay
// and the random throttling the options ask for.
func newHandler(w *fleettest.World, o options) *awsxtest.Handler {
	h := awsxtest.NewHandler(w)
	var mu sync.Mutex                                 // the source is not safe for concurrent use
	source := rand.New(rand.NewPCG(o.seed+1, o.seed)) //nolint:gosec // repeatable fake delays, not a secret
	draw := func(n int64) int64 {
		mu.Lock()
		defer mu.Unlock()
		return source.Int64N(n)
	}
	if o.latency > 0 {
		h.Latency = func(r awsxtest.Request) time.Duration {
			if r.Service == "sts" {
				return 0
			}
			most := int64(o.latency)
			if r.Region == o.slowRegion {
				most *= 10
			}
			return time.Duration(draw(most))
		}
	}
	if o.throttle > 0 {
		h.Throttle = func(r awsxtest.Request) bool {
			return r.Service == "eks" && float64(draw(1_000_000))/1e6 < o.throttle
		}
	}
	return h
}

// kubeServers are the Kubernetes API servers of a fleet, one per cluster.
type kubeServers struct {
	servers []*kubetest.Server
	// names and regions are the clusters, in the order of the servers.
	names, regions []string
}

// serveKube starts one API server for each cluster of w, which sets the
// cluster's EKS endpoint to that server, so the product matches the
// kubeconfig to the cluster. A cluster with no region answers in
// defaultRegion.
func serveKube(w *fleettest.World, defaultRegion string) *kubeServers {
	var ks kubeServers
	w.View(func(s *fleettest.State) {
		for _, c := range s.Clusters {
			ks.names = append(ks.names, c.Name)
			ks.regions = append(ks.regions, cmp.Or(c.Region, defaultRegion))
		}
	})
	for i, name := range ks.names {
		ks.servers = append(ks.servers, kubetest.NewServer(w, ks.regions[i], name))
	}
	return &ks
}

// Close stops every server.
func (ks *kubeServers) Close() {
	for _, s := range ks.servers {
		s.Close()
	}
}

// writeKubeconfig writes one kubeconfig with a context per cluster, named
// after the cluster, as "aws eks update-kubeconfig" would name it with
// --alias. The first cluster is the current context.
func (ks *kubeServers) writeKubeconfig(path string) error {
	cfg := clientcmdapi.NewConfig()
	for i, s := range ks.servers {
		name := ks.names[i]
		cfg.Clusters[name] = &clientcmdapi.Cluster{Server: s.URL}
		cfg.AuthInfos[name] = &clientcmdapi.AuthInfo{Token: "fake-token"}
		cfg.Contexts[name] = &clientcmdapi.Context{Cluster: name, AuthInfo: name}
		if i == 0 {
			cfg.CurrentContext = name
		}
	}
	data, err := clientcmd.Write(*cfg)
	if err != nil {
		return fmt.Errorf("encoding the kubeconfig: %w", err)
	}
	return os.WriteFile(path, data, 0o600)
}
