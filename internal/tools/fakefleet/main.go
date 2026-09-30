// Command fakefleet serves a large fake EKS fleet for testing refresh at
// fleet scale with no AWS account: many clusters over many regions, slow
// and throttled calls, a region closed to the credentials, and changes in
// flight. It prints the environment to point refresh at it, then serves
// until interrupted. It is a dev tool, not linked into the refresh binary.
//
//	go run ./internal/tools/fakefleet -clusters 60 -regions 8 > fleet.env
//	source fleet.env && refresh status -A
package main

import (
	"flag"
	"fmt"
	"math/rand/v2"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/dantech2000/refresh/internal/mocks/fakeaws"
)

func main() { os.Exit(run()) }

// run serves the fleet until interrupted and returns the exit code, so the
// deferred cleanup runs before the process exits.
func run() int {
	var (
		clusters  = flag.Int("clusters", 60, "clusters in the fleet")
		regions   = flag.Int("regions", 8, "regions the clusters spread over")
		latency   = flag.Duration("latency", 250*time.Millisecond, "the most a call waits (each call waits a random part of it)")
		slow      = flag.String("slow-region", "ap-southeast-2", "a region whose calls take up to 10x -latency (\"\" for none)")
		closed    = flag.String("closed-region", "me-central-1", "a region closed to the credentials (\"\" for none)")
		throttle  = flag.Float64("throttle", 0.03, "the share of EKS calls answered with ThrottlingException")
		seed      = flag.Uint64("seed", 1, "the random seed for the fleet and the delays")
		configDir = flag.String("dir", "", "where to write the empty AWS config and kubeconfig (default: a temp dir)")
	)
	flag.Parse()

	all := []string{"us-east-1", "us-west-2", "eu-west-1", "eu-central-1", "ap-southeast-2", "ap-northeast-1", "ca-central-1", "sa-east-1", "eu-north-1", "ap-south-1"}
	if *regions < 1 || *regions > len(all) {
		fmt.Fprintf(os.Stderr, "-regions must be 1..%d\n", len(all))
		return 2
	}
	used := all[:*regions]

	rng := rand.New(rand.NewPCG(*seed, *seed^0x9e3779b97f4a7c15)) //nolint:gosec // a repeatable fake fleet, not a secret
	fleet := buildFleet(rng, *clusters, used)

	s, stop := fakeaws.Start(fleet...)
	defer stop()
	s.SetSupportedVersions("1.31", "1.32", "1.33", "1.34", "1.35", "1.36")
	s.SetVersionStatus("1.31", "EXTENDED_SUPPORT")
	s.SetVersionStatus("1.32", "EXTENDED_SUPPORT")

	var mu sync.Mutex                              // the delay source is not safe for concurrent use
	delay := rand.New(rand.NewPCG(*seed+1, *seed)) //nolint:gosec // repeatable fake delays, not a secret
	draw := func(n int64) int64 {
		mu.Lock()
		defer mu.Unlock()
		return delay.Int64N(n)
	}
	closedRegion, slowRegion := *closed, *slow
	s.SetLatency(func(region, service, _ string) time.Duration {
		if service == "sts" || *latency <= 0 {
			return 0
		}
		most := int64(*latency)
		if region == slowRegion {
			most *= 10
		}
		return time.Duration(draw(most))
	})
	if *throttle > 0 {
		s.SetThrottle(func(service, _ string) bool { return service == "eks" && float64(draw(1_000_000))/1e6 < *throttle })
	}
	s.FailRegions(func(region string) string {
		if region == closedRegion {
			return "UnrecognizedClientException"
		}
		return ""
	})

	dir := *configDir
	if dir == "" {
		d, err := os.MkdirTemp("", "fakefleet")
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		dir = d
		defer func() { _ = os.RemoveAll(d) }()
	}
	empty := filepath.Join(dir, "empty")
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	for _, kv := range [][2]string{
		{"AWS_ENDPOINT_URL", s.URL},
		{"AWS_ACCESS_KEY_ID", "AKIDFAKEFAKEFAKE"},
		{"AWS_SECRET_ACCESS_KEY", "fake-secret"},
		{"AWS_REGION", used[0]},
		{"AWS_CONFIG_FILE", empty},
		{"AWS_SHARED_CREDENTIALS_FILE", empty},
		{"AWS_EC2_METADATA_DISABLED", "true"},
		{"AWS_PROFILE", ""},
		{"KUBECONFIG", filepath.Join(dir, "no-kubeconfig")},
		{"REFRESH_CONFIG_HOME", dir},
		{"REFRESH_EKS_REGIONS", strings.Join(used, ",")},
	} {
		fmt.Printf("export %s=%q\n", kv[0], kv[1])
	}
	fmt.Fprintf(os.Stderr, "fakefleet: %d clusters in %d regions at %s (slow %s, closed %s, %.0f%% throttled); Ctrl+C to stop\n",
		len(fleet), len(used), s.URL, orNone(slowRegion), orNone(closedRegion), *throttle*100)

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	<-sig
	return 0
}

func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}

// buildFleet makes n clusters spread over regions: a mix of versions (some
// in extended support), one to four nodegroups each (some stale, a few
// UPDATING), and the usual add-ons (some behind).
func buildFleet(rng *rand.Rand, n int, regions []string) []*fakeaws.Cluster {
	versions := []string{"1.31", "1.32", "1.33", "1.34", "1.34", "1.35", "1.35", "1.36"}
	teams := []string{"payments", "search", "checkout", "identity", "data", "ml", "edge", "platform", "billing", "catalog", "orders", "notify"}
	envs := []string{"prod", "staging", "dev"}
	amiTypes := []string{"AL2023_x86_64_STANDARD", "AL2023_ARM_64_STANDARD", "BOTTLEROCKET_x86_64", "AL2_x86_64"}
	out := make([]*fakeaws.Cluster, 0, n)
	for i := range n {
		v := versions[rng.IntN(len(versions))]
		name := fmt.Sprintf("%s-%s-%02d", envs[i%len(envs)], teams[rng.IntN(len(teams))], i)
		c := &fakeaws.Cluster{Name: name, Version: v, Region: regions[i%len(regions)]}
		for j := range 1 + rng.IntN(4) {
			ngv := v
			if rng.IntN(4) == 0 {
				ngv = prevMinor(v) // a nodegroup left behind
			}
			ami := amiTypes[rng.IntN(len(amiTypes))]
			if ami == "AL2_x86_64" && minor(ngv) > 32 {
				ami = "AL2023_x86_64_STANDARD"
			}
			ng := &fakeaws.Nodegroup{Name: fmt.Sprintf("ng-%c", 'a'+j), Version: ngv, AmiType: ami, Desired: 2 + rng.Int32N(8), Min: 1, Max: 12}
			if rng.IntN(20) == 0 {
				ng.Status = "UPDATING"
			}
			c.Nodegroups = append(c.Nodegroups, ng)
		}
		for _, a := range []string{"vpc-cni", "coredns", "kube-proxy", "eks-pod-identity-agent"} {
			latest := "v" + v + ".2-eksbuild.1"
			cur := latest
			if rng.IntN(3) == 0 {
				cur = "v" + v + ".1-eksbuild.1" // behind
			}
			c.Addons = append(c.Addons, &fakeaws.Addon{Name: a, Version: cur, Status: "ACTIVE", Available: []string{latest, "v" + v + ".1-eksbuild.1"}})
		}
		out = append(out, c)
	}
	return out
}

func minor(v string) int {
	var maj, mnr int
	_, _ = fmt.Sscanf(v, "%d.%d", &maj, &mnr)
	return mnr
}

func prevMinor(v string) string { return fmt.Sprintf("1.%d", minor(v)-1) }
