package status_test

import (
	"context"
	"flag"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/dantech2000/refresh/internal/apidoc"
	"github.com/dantech2000/refresh/internal/awsx/awsxtest"
	"github.com/dantech2000/refresh/internal/cmd/cmdtest"
	"github.com/dantech2000/refresh/internal/cmdutil"
	"github.com/dantech2000/refresh/internal/cmdutil/cmdutiltest"
	"github.com/dantech2000/refresh/internal/fleet/fleettest"
)

var update = flag.Bool("update", false, "write the golden files in testdata")

const (
	east = "us-east-1"
	eu   = "eu-west-1"
)

func day(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 0, 0, 0, 0, time.UTC) }

// seed adds the catalog that every world of these tests has: the support
// dates of the versions and the versions of the add-ons. The clock of the
// test factory reads 2026-10-01.
func seed(s *fleettest.State) {
	s.ClusterVersions = []fleettest.ClusterVersion{
		{Version: "1.34", EndOfStandardSupport: day(2027, 6, 1), EndOfExtendedSupport: day(2028, 6, 1)},
		{Version: "1.33", EndOfStandardSupport: day(2026, 12, 1), EndOfExtendedSupport: day(2027, 12, 1)},
		{Version: "1.31", EndOfStandardSupport: day(2026, 6, 1), EndOfExtendedSupport: day(2027, 6, 1)},
		{Version: "1.29", EndOfStandardSupport: day(2025, 3, 23), EndOfExtendedSupport: day(2026, 3, 23)},
	}
	for _, v := range []string{"1.29", "1.31", "1.33", "1.34"} {
		s.AddonVersions = append(s.AddonVersions,
			fleettest.AddonVersion{Addon: "vpc-cni", Version: "v1.20.0-eksbuild.1", ClusterVersions: []string{v}},
			fleettest.AddonVersion{Addon: "coredns", Version: "v1.11.3-eksbuild.2", ClusterVersions: []string{v}},
			fleettest.AddonVersion{Addon: "kube-proxy", Version: "v1.33.0-eksbuild.2", ClusterVersions: []string{v}},
		)
	}
}

// current returns a cluster with nothing out of date.
func current(name, region string) *fleettest.Cluster {
	return &fleettest.Cluster{
		Name: name, Region: region, Version: "1.34",
		Nodegroups: []*fleettest.Nodegroup{{Name: "ng", Version: "1.34"}},
		Addons:     []*fleettest.Addon{{Name: "vpc-cni", Version: "v1.20.0-eksbuild.1"}},
	}
}

// newFake returns a fake AWS with the fleet of the golden files.
func newFake(t *testing.T) *cmdtest.Fake {
	t.Helper()
	world := fleettest.New()
	world.Edit(func(s *fleettest.State) {
		seed(s)
		s.Clusters = append(s.Clusters,
			current("prod-current", east),
			&fleettest.Cluster{
				Name: "prod-stale", Region: east, Version: "1.33",
				Nodegroups: []*fleettest.Nodegroup{
					{Name: "web", Version: "1.33", AutoScalingGroups: []string{"asg-web"}},
					{Name: "api", Version: "1.33", AutoScalingGroups: []string{"asg-api"}},
					{Name: "old", Version: "1.32", AutoScalingGroups: []string{"asg-old"}},
				},
				Addons: []*fleettest.Addon{
					{Name: "vpc-cni", Version: "v1.19.0-eksbuild.1"},
					{Name: "coredns", Version: "v1.11.1-eksbuild.1"},
					{Name: "kube-proxy", Version: "v1.32.0-eksbuild.1"},
				},
				HealthIssues: []fleettest.Issue{{Code: "Ec2SubnetInvalidConfiguration", Message: "a subnet is gone"}},
			},
			&fleettest.Cluster{Name: "legacy", Region: east, Version: "1.31", SupportType: "EXTENDED",
				Nodegroups: []*fleettest.Nodegroup{{Name: "ng", Version: "1.31"}}},
			&fleettest.Cluster{Name: "ancient", Region: eu, Version: "1.29"},
			&fleettest.Cluster{Name: "auto", Region: eu, Version: "1.34", AutoMode: true},
			&fleettest.Cluster{Name: "karpenter", Region: eu, Version: "1.34"},
		)
		s.AutoScalingGroups = []fleettest.AutoScalingGroup{
			{Name: "asg-web", InstanceIDs: []string{"i-web"}},
			{Name: "asg-api", InstanceIDs: []string{"i-api"}},
			{Name: "asg-old", InstanceIDs: []string{"i-old"}},
		}
		s.Instances = []fleettest.Instance{
			{ID: "i-web", ImageID: "ami-stale-1"},
			{ID: "i-api", ImageID: "ami-latest133"},
			{ID: "i-old", ImageID: "ami-stale-2"},
			{ID: "i-karp", Region: eu, ImageID: "ami-x", Tags: map[string]string{"eks:eks-cluster-name": "karpenter", "karpenter.sh/nodepool": "default"}},
		}
		s.Images = []fleettest.Image{
			{ID: "ami-stale-1", Created: day(2026, 8, 17)},
			{ID: "ami-stale-2", Created: day(2026, 5, 1)},
		}
	})
	return cmdtest.NewFake(t, world, east)
}

var twoRegions = []string{"status", "-r", east + "," + eu}

func TestStatus_Formats(t *testing.T) {
	for _, tt := range []struct{ format, file string }{
		{"table", "status.txt"}, {"plain", "status.tsv"}, {"json", "status.json"}, {"yaml", "status.yaml"},
	} {
		t.Run(tt.format, func(t *testing.T) {
			e := newFake(t)
			err := e.Run(cmdutiltest.Context(t), append(twoRegions, "-o", tt.format)...)
			// A cluster is in extended support and one is unsupported.
			if got := cmdutil.ExitCode(err); got != cmdutil.ExitBlocked || (err != nil && err.Error() != "") {
				t.Errorf("exit code = %d, error %q, want %d and no message", got, err, cmdutil.ExitBlocked)
			}
			if e.Err.Len() != 0 {
				t.Errorf("stderr = %q, want nothing", e.Err.String())
			}
			cmdutiltest.Golden(t, tt.file, e.Out.String(), *update)
			switch tt.format {
			case "json":
				cmdutiltest.ValidateJSON(t, apidoc.KindFleetStatus, e.Out.Bytes())
			case "yaml":
				cmdutiltest.ValidateYAML(t, apidoc.KindFleetStatus, e.Out.Bytes())
			}
		})
	}
}

// The exit code is the highest that applies: incomplete data (4), a
// cluster out of standard support (3), something out of date (2).
func TestStatus_ExitCodes(t *testing.T) {
	stale := func() *fleettest.Cluster {
		c := current("stale", east)
		c.Addons[0].Version = "v1.19.0-eksbuild.1"
		return c
	}
	extended := func() *fleettest.Cluster {
		c := current("extended", east)
		c.Version, c.Nodegroups[0].Version = "1.31", "1.31"
		return c
	}
	issue := func() *fleettest.Cluster {
		c := current("issue", east)
		c.HealthIssues = []fleettest.Issue{{Code: "X", Message: "y"}}
		return c
	}
	behind := func() *fleettest.Cluster {
		c := current("behind", east)
		c.Nodegroups[0].Version = "1.33"
		return c
	}
	deny := func(w *fleettest.World) {
		w.SetScopedFault(fleettest.Scope{Operation: fleettest.EKSDescribeNodegroup}, fleettest.Fault{Deny: true})
	}
	tests := []struct {
		name     string
		clusters []*fleettest.Cluster
		fault    func(*fleettest.World)
		want     int
	}{
		{name: "everything is current", clusters: []*fleettest.Cluster{current("a", east), current("b", east)}, want: cmdutil.ExitOK},
		{name: "no clusters", want: cmdutil.ExitOK},
		{name: "an add-on behind", clusters: []*fleettest.Cluster{current("a", east), stale()}, want: cmdutil.ExitNeedsAttention},
		{name: "a control-plane issue", clusters: []*fleettest.Cluster{issue()}, want: cmdutil.ExitNeedsAttention},
		{name: "a nodegroup behind the control plane", clusters: []*fleettest.Cluster{behind()}, want: cmdutil.ExitNeedsAttention},
		{name: "extended support", clusters: []*fleettest.Cluster{extended()}, want: cmdutil.ExitBlocked},
		{name: "extended support wins over stale", clusters: []*fleettest.Cluster{stale(), extended()}, want: cmdutil.ExitBlocked},
		{name: "incomplete data wins over everything", clusters: []*fleettest.Cluster{stale(), extended()}, fault: deny, want: cmdutil.ExitIncomplete},
		{name: "incomplete data alone", clusters: []*fleettest.Cluster{current("a", east)}, fault: deny, want: cmdutil.ExitIncomplete},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			world := fleettest.New()
			world.Edit(func(s *fleettest.State) {
				seed(s)
				s.Clusters = tt.clusters
			})
			e := cmdtest.NewFake(t, world, east)
			if tt.fault != nil {
				tt.fault(world)
			}
			err := e.Run(cmdutiltest.Context(t), "status", "-o", "json")
			if got := cmdutil.ExitCode(err); got != tt.want {
				t.Errorf("exit code = %d (%v), want %d\n%s", got, err, tt.want, e.Out.String())
			}
			cmdutiltest.ValidateJSON(t, apidoc.KindFleetStatus, e.Out.Bytes())
		})
	}
}

// A read that only loses an extra is not a failure: the output marks what
// is missing, and the exit code is the one of the fleet. A user with a
// narrow IAM policy does not get exit 4 from every run.
func TestStatus_AdvisoryReadsAreNotFailures(t *testing.T) {
	e := newFake(t)
	for _, op := range []fleettest.Operation{fleettest.EKSDescribeClusterVersions, fleettest.EC2DescribeImages} {
		e.World.SetFault(op, fleettest.Fault{Deny: true})
	}
	// The Karpenter probe: the instance calls of the region with the
	// clusters that have no nodegroup.
	e.World.SetScopedFault(fleettest.Scope{Region: eu, Operation: fleettest.EC2DescribeInstances}, fleettest.Fault{Deny: true})

	err := e.Run(cmdutiltest.Context(t), append(twoRegions, "-o", "plain")...)
	if got := cmdutil.ExitCode(err); got != cmdutil.ExitBlocked {
		t.Errorf("exit code = %d (%v), want %d: the same as with every read", got, err, cmdutil.ExitBlocked)
	}
	if e.Err.Len() != 0 {
		t.Errorf("stderr = %q, want no warning", e.Err.String())
	}
	cmdutiltest.Golden(t, "advisory.tsv", e.Out.String(), *update)
}

func TestStatus_Partial(t *testing.T) {
	e := newFake(t)
	e.World.SetFault(fleettest.SSMGetParameter, fleettest.Fault{Deny: true})
	e.World.SetScopedFault(fleettest.Scope{Operation: fleettest.EKSDescribeAddonVersions, Cluster: ""}, fleettest.Fault{Throttle: 1000})
	err := e.Run(cmdutiltest.Context(t), append(twoRegions, "-o", "table")...)
	if got := cmdutil.ExitCode(err); got != cmdutil.ExitIncomplete {
		t.Errorf("exit code = %d (%v), want %d", got, err, cmdutil.ExitIncomplete)
	}
	cmdutiltest.Golden(t, "partial.txt", e.Out.String(), *update)
}

func TestStatus_SortAndFilter(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want []string
	}{
		{"by name", nil, []string{"ancient", "auto", "karpenter", "legacy", "prod-current", "prod-stale"}},
		{"by what is stale, most first", []string{"--sort", "stale", "--desc"}, []string{"prod-stale", "ancient", "auto", "karpenter", "legacy", "prod-current"}},
		{"by support, most urgent first", []string{"--sort", "support", "--desc"}, []string{"ancient", "legacy", "auto", "karpenter", "prod-current", "prod-stale"}},
		{"by version", []string{"--sort", "version"}, []string{"ancient", "legacy", "prod-stale", "auto", "karpenter", "prod-current"}},
		{"extended support", []string{"-f", "support=extended"}, []string{"legacy"}},
		{"a version and a region", []string{"-f", "version=1.34", "-f", "region=eu-*"}, []string{"auto", "karpenter"}},
		{"a name", []string{"-f", "name=prod-*"}, []string{"prod-current", "prod-stale"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newFake(t)
			_ = e.Run(cmdutiltest.Context(t), append(append([]string{}, twoRegions...), append(tt.args, "-o", "plain")...)...)
			var got []string
			for _, line := range strings.Split(strings.TrimSpace(e.Out.String()), "\n")[1:] {
				got = append(got, strings.Split(line, "\t")[0])
			}
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("clusters (-want +got):\n%s", diff)
			}
		})
	}
}

// The waves of one cluster with a stale AMI: six sequential round trips,
// and five without the age of the AMI.
func TestStatus_Waves(t *testing.T) {
	world := fleettest.New()
	world.Edit(func(s *fleettest.State) {
		seed(s)
		s.Clusters = []*fleettest.Cluster{{
			Name: "prod", Region: east, Version: "1.33",
			Nodegroups: []*fleettest.Nodegroup{{Name: "web", Version: "1.33", AutoScalingGroups: []string{"asg-web"}}},
			Addons:     []*fleettest.Addon{{Name: "vpc-cni", Version: "v1.19.0-eksbuild.1"}},
		}}
		s.AutoScalingGroups = []fleettest.AutoScalingGroup{{Name: "asg-web", InstanceIDs: []string{"i-web"}}}
		s.Instances = []fleettest.Instance{{ID: "i-web", ImageID: "ami-stale"}}
		s.Images = []fleettest.Image{{ID: "ami-stale", Created: day(2026, 8, 1)}}
	})
	e := cmdtest.NewFake(t, world, east)
	gate := awsxtest.NewGate(t, e.Server.Handler)
	done := make(chan error, 1)
	go func() { done <- e.Run(cmdutiltest.Context(t), "status", "-o", "json") }()

	gate.Wave(fleettest.EKSListClusters)
	gate.Wave(fleettest.EKSDescribeCluster, fleettest.EKSListNodegroups, fleettest.EKSListAddons, fleettest.EKSDescribeClusterVersions)
	gate.Wave(fleettest.EKSDescribeNodegroup, fleettest.EKSDescribeAddon, fleettest.EKSDescribeAddonVersions)
	gate.Wave(fleettest.SSMGetParameter, fleettest.AutoScalingDescribeAutoScalingGroups)
	gate.Wave(fleettest.EC2DescribeInstances)
	gate.Wave(fleettest.EC2DescribeImages)
	gate.Done()
	if err := <-done; cmdutil.ExitCode(err) != cmdutil.ExitNeedsAttention {
		t.Fatalf("exit code = %d (%v), want %d", cmdutil.ExitCode(err), err, cmdutil.ExitNeedsAttention)
	}
}

// bigFleet returns a world of clusters spread over regions, each with two
// nodegroups and four add-ons, as a large account has.
func bigFleet(clusters int, regions []string) *fleettest.World {
	world := fleettest.New()
	world.Edit(func(s *fleettest.State) {
		seed(s)
		for i := range clusters {
			region := regions[i%len(regions)]
			name := fmt.Sprintf("c%03d", i)
			c := &fleettest.Cluster{Name: name, Region: region, Version: "1.33", Addons: []*fleettest.Addon{
				{Name: "vpc-cni", Version: "v1.20.0-eksbuild.1"}, {Name: "coredns", Version: "v1.11.3-eksbuild.2"},
				{Name: "kube-proxy", Version: "v1.33.0-eksbuild.2"}, {Name: "ebs-csi", Version: "v1.0.0"},
			}}
			for _, ng := range []string{"a", "b"} {
				asg := "asg-" + name + "-" + ng
				c.Nodegroups = append(c.Nodegroups, &fleettest.Nodegroup{Name: ng, Version: "1.33", AutoScalingGroups: []string{asg}})
				s.AutoScalingGroups = append(s.AutoScalingGroups, fleettest.AutoScalingGroup{Region: region, Name: asg, InstanceIDs: []string{"i-" + name + ng}})
				s.Instances = append(s.Instances, fleettest.Instance{Region: region, ID: "i-" + name + ng, ImageID: "ami-latest133"})
			}
			s.Clusters = append(s.Clusters, c)
		}
	})
	return world
}

var eightRegions = []string{"us-east-1", "us-west-2", "eu-west-1", "eu-central-1", "ap-southeast-2", "ap-northeast-1", "ca-central-1", "sa-east-1"}

// distinctCalls returns the number of different calls in the request log.
// With some hundred connections to one local server, the HTTP transport
// now and then sends a request again on a new connection after the server
// closed an idle one, and the log has that request twice.
func distinctCalls(e *cmdtest.Fake) int {
	seen := map[string]bool{}
	for _, r := range e.Server.Requests() {
		seen[fmt.Sprintf("%s %s %v", r.Operation, r.Region, r.Input)] = true
	}
	return len(seen)
}

// holdUntilFull makes the server hold the requests of each region until
// that region has limit requests in flight, and lets everything through
// from then on. Each region then reaches the limit, and the test does not
// depend on how fast the machine is.
func holdUntilFull(e *cmdtest.Fake, regions []string, limit int) {
	var mu sync.Mutex
	waiting := map[string]int{}
	full := map[string]chan struct{}{}
	for _, r := range regions {
		full[r] = make(chan struct{})
	}
	e.Server.OnRequest = func(ctx context.Context, r awsxtest.Request) {
		// The list call goes through: nothing else starts before it is
		// answered, so the region would never fill.
		if r.Operation == fleettest.EKSListClusters {
			return
		}
		mu.Lock()
		waiting[r.Region]++
		if waiting[r.Region] == limit {
			close(full[r.Region])
		}
		reached := full[r.Region]
		mu.Unlock()
		select {
		case <-reached:
		case <-ctx.Done():
		}
	}
}

// checkPeak fails the test when a region did not come close to the default
// request limit of 32, or passed it. The server's count is not exact to
// the request. It counts a request until its answer is written, which can
// be a moment after the client read the answer and sent the next request:
// one above the limit. And a request that the transport sent again after
// a connection closed arrived twice for holdUntilFull: a few below.
func checkPeak(t *testing.T, e *cmdtest.Fake, region string) {
	t.Helper()
	if peak := e.Server.PeakConcurrency("", region); peak < 28 || peak > 33 {
		t.Errorf("%s: peak requests in flight = %d, want the default limit of 32", region, peak)
	}
}

// A sweep of 200 clusters in 8 regions: the number of calls is the one
// that the design gives, the shared lookups are made once for a region,
// and every region reaches the request limit and does not pass it.
func TestStatus_LargeFleetCallsAndLimit(t *testing.T) {
	e := cmdtest.NewFake(t, bigFleet(200, eightRegions), east)
	// 256 requests in flight to one host: over HTTP/2, as against AWS.
	cmdutiltest.UseAWS(e.Factory, e.Server.MultiplexedConfig(east))
	holdUntilFull(e, eightRegions, 32)
	if err := e.Run(cmdutiltest.Context(t), "status", "-r", strings.Join(eightRegions, ","), "-o", "json"); err != nil {
		t.Fatalf("run: %v", err)
	}
	// For a region: the list and the calendar, one SSM lookup, and one
	// version lookup for each of the 4 add-ons. For a cluster: itself,
	// 2 lists, 2 nodegroups, 4 add-ons, the groups, and the instances.
	const perRegion, perCluster = 2 + 1 + 4, 1 + 2 + 2 + 4 + 1 + 1
	if got, want := distinctCalls(e), 8*perRegion+200*perCluster; got != want {
		t.Errorf("calls = %d, want %d", got, want)
	}
	for _, region := range eightRegions {
		checkPeak(t, e, region)
	}
	t.Logf("calls: %d; peak requests in flight over all regions: %d", distinctCalls(e), e.Server.PeakConcurrency("", ""))
}

// Throttling at the limit: once a region has had 32 requests in flight,
// EKS throttles one call in five, once each. The retries absorb it, and
// the run completes with no failure.
func TestStatus_ThrottledAtTheLimit(t *testing.T) {
	e := cmdtest.NewFake(t, bigFleet(200, eightRegions), east)
	// 256 requests in flight to one host: over HTTP/2, as against AWS.
	cmdutiltest.UseAWS(e.Factory, e.Server.MultiplexedConfig(east))
	holdUntilFull(e, eightRegions, 32)
	var (
		mu        sync.Mutex
		calls     int
		throttled = map[string]bool{}
	)
	e.Server.Throttle = func(r awsxtest.Request) bool {
		mu.Lock()
		defer mu.Unlock()
		key := fmt.Sprintf("%s %s %v", r.Operation, r.Region, r.Input)
		if r.Service != "eks" || throttled[key] || e.Server.PeakConcurrency("", r.Region) < 32 {
			return false
		}
		calls++
		if calls%5 != 0 {
			return false
		}
		throttled[key] = true
		return true
	}
	err := e.Run(cmdutiltest.Context(t), "status", "-r", strings.Join(eightRegions, ","), "-o", "json")
	if err != nil {
		t.Fatalf("run: %v\n%s", err, e.Err.String())
	}
	if !strings.Contains(e.Out.String(), `"failures": []`) {
		t.Errorf("the document has failures")
	}
	if len(throttled) < 100 {
		t.Errorf("%d calls were throttled, want some hundred: the regions did not stay at the limit", len(throttled))
	}
	for _, region := range eightRegions {
		checkPeak(t, e, region)
	}
	t.Logf("%d calls were throttled once each; the run completed with no failure", len(throttled))
}

// A deadline in the middle of a sweep is a partial result with exit 4.
func TestStatus_Deadline(t *testing.T) {
	e := newFake(t)
	ctx, expire := cmdutiltest.Deadline(cmdutiltest.Context(t))
	e.Server.OnRequest = func(hctx context.Context, r awsxtest.Request) {
		if r.Operation == fleettest.EKSDescribeAddon {
			expire()
			<-hctx.Done()
		}
	}
	// One request slot: the calls go out one at a time, in the order that
	// they asked for the slot. The cluster asked before any call of the
	// second wave, so it is answered before the run is cut short.
	err := e.Run(ctx, "status", "-o", "json", "--max-concurrency", "1")
	if got := cmdutil.ExitCode(err); got != cmdutil.ExitIncomplete {
		t.Fatalf("exit code = %d (%v), want %d", got, err, cmdutil.ExitIncomplete)
	}
	cmdutiltest.ValidateJSON(t, apidoc.KindFleetStatus, e.Out.Bytes())
}

func TestStatus_InvalidInput(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"-A with -r", []string{"status", "-A", "-r", east}, "cannot be used together"},
		{"an unknown filter key", []string{"status", "-f", "stale=1"}, `unknown key "stale"; the keys are name, region, version, support`},
		{"an unknown sort key", []string{"status", "--sort", "cluster"}, "must be one of: name, region, version, support, stale"},
		{"an argument", []string{"status", "prod"}, `unknown command "prod"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newFake(t)
			err := e.Run(cmdutiltest.Context(t), tt.args...)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want one that contains %q", err, tt.want)
			}
			if n := len(e.Server.Requests()); n != 0 {
				t.Errorf("requests = %v, want none", e.Operations())
			}
		})
	}
}

// On a terminal the table appears as the regions answer, and the final
// table is printed once. A table taller than the terminal gets one status
// line on stderr in place of the redraw.
func TestStatus_TerminalProgress(t *testing.T) {
	plain := newFake(t)
	_ = plain.Run(cmdutiltest.Context(t), append(twoRegions, "-o", "table")...)

	e := newFake(t)
	e.Factory.IOStreams.OutTTY, e.Factory.IOStreams.ErrTTY = true, true
	e.Factory.IOStreams.TermSize = func() (int, int) { return 200, 40 }
	_ = e.Run(cmdutiltest.Context(t), twoRegions...)
	out := e.Out.String()
	if n := strings.Count(out, "\x1b[J"); n < 2 {
		t.Errorf("stdout has %d redraws, want one for each region and the clear before the final table:\n%q", n, out)
	}
	final := out[strings.LastIndex(out, "\x1b[J")+len("\x1b[J"):]
	if diff := cmp.Diff(plain.Out.String(), final); diff != "" {
		t.Errorf("the final table is not the table of a run without a terminal (-plain +terminal):\n%s", diff)
	}

	short := newFake(t)
	short.Factory.IOStreams.OutTTY, short.Factory.IOStreams.ErrTTY = true, true
	short.Factory.IOStreams.TermSize = func() (int, int) { return 200, 5 }
	_ = short.Run(cmdutiltest.Context(t), twoRegions...)
	if strings.Contains(short.Out.String(), "\x1b[") {
		t.Errorf("stdout has cursor codes on a terminal that is too short:\n%q", short.Out.String())
	}
	if !strings.Contains(short.Err.String(), "of 2 regions") {
		t.Errorf("stderr = %q, want the status line", short.Err.String())
	}
}
