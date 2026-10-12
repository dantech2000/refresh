// Command fakefleet serves a fake EKS fleet for trying refresh with no AWS
// account: many clusters over many regions, slow and throttled calls, and a
// region closed to the credentials. It prints the environment that points
// refresh at it, then serves until interrupted.
//
//	go run ./tools/fakefleet -clusters 60 -regions 8 > fleet.env
//	source fleet.env && refresh status -A
//
// With -world it serves the fleet in a JSON file instead of a generated
// one. The file holds a fleettest.State.
package main

import (
	"bytes"
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
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/dantech2000/refresh/internal/awsx/awsxtest"
	"github.com/dantech2000/refresh/internal/fleet/fleettest"
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
}

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
	fs.StringVar(&o.dir, "dir", "", "where to write the empty AWS config and kubeconfig (default: a temp dir)")
	fs.StringVar(&o.worldFile, "world", "", "a JSON file with the fleet to serve, in place of a generated one")
	fs.StringVar(&o.addr, "addr", "127.0.0.1:0", "the address to listen on")
	if err := fs.Parse(args); err != nil {
		return o, err
	}
	if o.regions < 1 || o.regions > len(allRegions) {
		return o, fmt.Errorf("-regions must be 1..%d", len(allRegions))
	}
	if o.throttle < 0 || o.throttle > 1 {
		return o, errors.New("-throttle must be 0..1")
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
	world, regions, err := newWorld(o)
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
	}
	empty := filepath.Join(dir, "empty")
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		return err
	}

	var lc net.ListenConfig
	listener, err := lc.Listen(ctx, "tcp", o.addr)
	if err != nil {
		return err
	}
	url := "http://" + listener.Addr().String()
	for _, kv := range environment(url, dir, empty, regions) {
		if _, err := fmt.Fprintf(stdout, "export %s=%q\n", kv[0], kv[1]); err != nil {
			return err
		}
	}
	_, _ = fmt.Fprintf(stderr, "fakefleet: serving %s in %d regions (slow %s, closed %s, %.0f%% throttled); Ctrl+C to stop\n",
		url, len(regions), orNone(o.slowRegion), orNone(o.closedRegion), o.throttle*100)

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
	if err := server.Shutdown(shutdown); err != nil {
		return err
	}
	<-served
	return nil
}

// environment is what a refresh binary needs to reach the fleet and
// nothing else: no real credentials, profile, or kubeconfig.
func environment(url, dir, emptyFile string, regions []string) [][2]string {
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
		{"KUBECONFIG", filepath.Join(dir, "no-kubeconfig")},
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

// newWorld returns the fleet to serve and the regions it uses: the one in
// the -world file, or a generated one. The closed region rejects the
// credentials for every service.
func newWorld(o options) (*fleettest.World, []string, error) {
	var state fleettest.State
	regions := allRegions[:o.regions]
	if o.worldFile != "" {
		data, err := os.ReadFile(o.worldFile)
		if err != nil {
			return nil, nil, err
		}
		dec := json.NewDecoder(bytes.NewReader(data))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&state); err != nil {
			return nil, nil, fmt.Errorf("reading %s: %w", o.worldFile, err)
		}
		if regions = clusterRegions(&state); len(regions) == 0 {
			regions = allRegions[:1]
		}
	} else {
		rng := rand.New(rand.NewPCG(o.seed, o.seed^0x9e3779b97f4a7c15)) //nolint:gosec // a repeatable fake fleet, not a secret
		state = generate(rng, o.clusters, regions)
	}
	w := fleettest.New()
	w.Edit(func(s *fleettest.State) { *s = state })
	if o.closedRegion != "" {
		w.SetScopedFault(fleettest.Scope{Region: o.closedRegion}, fleettest.Fault{Unauthenticated: true})
	}
	return w, regions, nil
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

// generate makes n clusters spread over regions: a mix of versions (some
// in extended support), one to four nodegroups each (some a minor behind, a
// few UPDATING), and the usual add-ons (some behind).
func generate(rng *rand.Rand, n int, regions []string) fleettest.State {
	const oldest, newest = 31, 36
	minors := []int{31, 32, 33, 34, 34, 35, 35, 36}
	teams := []string{"payments", "search", "checkout", "identity", "data", "ml", "edge", "platform", "billing", "catalog", "orders", "notify"}
	envs := []string{"prod", "staging", "dev"}
	amiTypes := []string{"AL2023_x86_64_STANDARD", "AL2023_ARM_64_STANDARD", "BOTTLEROCKET_x86_64", "AL2_x86_64"}
	addons := []string{"vpc-cni", "coredns", "kube-proxy", "eks-pod-identity-agent"}
	version := func(minor int) string { return fmt.Sprintf("1.%d", minor) }

	var s fleettest.State
	for minor := oldest; minor <= newest; minor++ {
		cv := fleettest.ClusterVersion{Version: version(minor)}
		if minor <= oldest+1 {
			cv.Status = fleettest.ExtendedSupport
		}
		s.ClusterVersions = append(s.ClusterVersions, cv)
		// The catalog by Kubernetes version, as EKS lists it: a 1.34
		// cluster never sees a build for 1.36.
		for _, a := range addons {
			for build := 2; build >= 1; build-- {
				s.AddonVersions = append(s.AddonVersions, fleettest.AddonVersion{
					Addon: a, Version: fmt.Sprintf("v%s.%d-eksbuild.1", version(minor), build),
					ClusterVersions: []string{version(minor)}, Default: build == 2,
				})
			}
		}
	}
	for i := range n {
		minor := minors[rng.IntN(len(minors))]
		c := &fleettest.Cluster{
			Name:    fmt.Sprintf("%s-%s-%02d", envs[i%len(envs)], teams[rng.IntN(len(teams))], i),
			Version: version(minor), Region: regions[i%len(regions)],
		}
		for j := range 1 + rng.IntN(4) {
			ngMinor := minor
			if rng.IntN(4) == 0 {
				ngMinor-- // a nodegroup left behind
			}
			ami := amiTypes[rng.IntN(len(amiTypes))]
			if ami == "AL2_x86_64" && ngMinor > 32 {
				ami = "AL2023_x86_64_STANDARD"
			}
			ng := &fleettest.Nodegroup{
				Name: fmt.Sprintf("ng-%c", 'a'+j), Version: version(ngMinor), AMIType: ami,
				Desired: 2 + rng.Int32N(8), Min: 1, Max: 12,
			}
			if rng.IntN(20) == 0 {
				ng.Status = fleettest.StatusUpdating
			}
			c.Nodegroups = append(c.Nodegroups, ng)
		}
		for _, a := range addons {
			build := 2
			if rng.IntN(3) == 0 {
				build = 1 // behind
			}
			c.Addons = append(c.Addons, &fleettest.Addon{Name: a, Version: fmt.Sprintf("v%s.%d-eksbuild.1", version(minor), build)})
		}
		s.Clusters = append(s.Clusters, c)
	}
	return s
}
