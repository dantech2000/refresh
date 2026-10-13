package fleet_test

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws/retry"
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"github.com/dantech2000/refresh/internal/apidoc"
	"github.com/dantech2000/refresh/internal/awsx"
	"github.com/dantech2000/refresh/internal/awsx/awsxtest"
	"github.com/dantech2000/refresh/internal/fleet"
	"github.com/dantech2000/refresh/internal/fleet/fleettest"
)

const (
	east = "us-east-1"
	west = "us-west-2"
	eu   = "eu-west-1"
)

// newFleet starts a fake AWS with the clusters, given as "region/name", and
// returns it with a provider that reaches it. limit is the request limit of
// the provider.
func newFleet(t *testing.T, limit int, clusters ...string) (*fleettest.World, *awsxtest.Server, *awsx.Provider) {
	t.Helper()
	world := fleettest.New()
	world.Edit(func(s *fleettest.State) {
		for _, c := range clusters {
			region, name, ok := strings.Cut(c, "/")
			if !ok {
				t.Fatalf("cluster %q: want region/name", c)
			}
			s.Clusters = append(s.Clusters, &fleettest.Cluster{Name: name, Region: region, Version: "1.33"})
		}
	})
	srv := awsxtest.NewServer(world)
	t.Cleanup(srv.Close)
	provider := awsx.NewProvider(srv.Config(east), func(o *awsx.ProviderOptions) {
		o.MaxInFlight = limit
		o.Backoff = retry.BackoffDelayerFunc(func(int, error) (time.Duration, error) { return 0, nil })
	})
	return world, srv, provider
}

func testContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// names returns "region/name" for each snapshot.
func names(snaps []*fleet.Snapshot) []string {
	out := make([]string, len(snaps))
	for i, s := range snaps {
		out[i] = s.Cluster.Region + "/" + s.Cluster.Name
	}
	return out
}

// failureKeys returns "Kind name reason operation" for each failure.
func failureKeys(fs []apidoc.Failure) []string {
	out := make([]string, len(fs))
	for i, f := range fs {
		out[i] = fmt.Sprintf("%s %s %s %s", f.Kind, f.Name, f.Reason, f.Operation)
	}
	return out
}

func TestSweep_ReadsEveryRegionInOrder(t *testing.T) {
	_, srv, provider := newFleet(t, 8, east+"/b", east+"/a", eu+"/c")
	var fetcher fleet.Fetcher
	var reported sync.Map
	res := fetcher.Sweep(testContext(t), provider, fleet.Regions{Names: []string{eu, west, east}}, fleet.SweepOptions{
		OnRegion: func(region string, snaps []*fleet.Snapshot) { reported.Store(region, len(snaps)) },
	})

	if diff := cmp.Diff([]string{eu, west, east}, res.Answered); diff != "" {
		t.Errorf("Answered (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff([]string{eu + "/c", east + "/a", east + "/b"}, names(res.Snapshots)); diff != "" {
		t.Errorf("Snapshots (-want +got):\n%s", diff)
	}
	if len(res.Failures) != 0 || len(res.Skipped) != 0 {
		t.Errorf("Failures = %v, Skipped = %v, want none", res.Failures, res.Skipped)
	}
	for region, want := range map[string]int{eu: 1, west: 0, east: 2} {
		if got, ok := reported.Load(region); !ok || got != want {
			t.Errorf("OnRegion(%s) got %v clusters, want %d", region, got, want)
		}
	}
	snap := res.Snapshots[1]
	if snap.Have.Cluster != fleet.Collected || snap.Cluster.Status != "ACTIVE" || snap.Cluster.Version != (fleet.Version{Major: 1, Minor: 33}) {
		t.Errorf("snapshot of %s: Have = %v, Status = %q, Version = %v", snap.Cluster.Name, snap.Have.Cluster, snap.Cluster.Status, snap.Cluster.Version)
	}
	// One list call for each region, and one describe call for each cluster.
	if n := len(srv.Requests()); n != 6 {
		t.Errorf("requests = %d, want 6", n)
	}
}

func TestSweep_MatchSavesTheDescribeCalls(t *testing.T) {
	_, srv, provider := newFleet(t, 8, east+"/prod-a", east+"/dev-a", east+"/prod-b")
	var fetcher fleet.Fetcher
	res := fetcher.Sweep(testContext(t), provider, fleet.Regions{Names: []string{east}}, fleet.SweepOptions{
		Match: func(_, name string) bool { return name != "dev-a" },
	})
	if diff := cmp.Diff([]string{east + "/prod-a", east + "/prod-b"}, names(res.Snapshots)); diff != "" {
		t.Errorf("Snapshots (-want +got):\n%s", diff)
	}
	describes := 0
	for _, r := range srv.Requests() {
		if r.Operation == fleettest.EKSDescribeCluster {
			describes++
		}
	}
	if describes != 2 {
		t.Errorf("DescribeCluster calls = %d, want 2", describes)
	}
}

// A default sweep leaves out a region that is closed to the credentials. It
// is not a failure.
func TestSweep_DefaultSweepSkipsAClosedRegion(t *testing.T) {
	world, _, provider := newFleet(t, 8, east+"/a")
	world.SetScopedFault(fleettest.Scope{Region: eu}, fleettest.Fault{Unauthenticated: true})
	var fetcher fleet.Fetcher
	res := fetcher.Sweep(testContext(t), provider, fleet.Regions{Names: []string{east, eu}, Default: true}, fleet.SweepOptions{})

	if diff := cmp.Diff([]string{east}, res.Answered); diff != "" {
		t.Errorf("Answered (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff([]string{eu}, res.Skipped); diff != "" {
		t.Errorf("Skipped (-want +got):\n%s", diff)
	}
	if len(res.Failures) != 0 {
		t.Errorf("Failures = %v, want none", res.Failures)
	}
}

// A region that the user named never disappears: closed, it is a failure.
func TestSweep_NamedClosedRegionFails(t *testing.T) {
	world, _, provider := newFleet(t, 8, east+"/a")
	world.SetScopedFault(fleettest.Scope{Region: eu}, fleettest.Fault{Unauthenticated: true})
	var fetcher fleet.Fetcher
	res := fetcher.Sweep(testContext(t), provider, fleet.Regions{Names: []string{east, eu}}, fleet.SweepOptions{})

	if len(res.Skipped) != 0 {
		t.Errorf("Skipped = %v, want none", res.Skipped)
	}
	want := []string{"Region eu-west-1 RegionUnavailable eks:ListClusters"}
	if diff := cmp.Diff(want, failureKeys(res.Failures)); diff != "" {
		t.Errorf("Failures (-want +got):\n%s", diff)
	}
	if len(res.Errs) != 1 || !awsx.IsRegionInaccessible(res.Errs[0]) {
		t.Errorf("Errs = %v, want the error of the region", res.Errs)
	}
	if f := res.Failures[0]; f.Region != eu || f.Retryable || f.AWSErrorCode == "" {
		t.Errorf("failure = %+v, want the region, not retryable, and the AWS code", f)
	}
}

// A default sweep still fails a region for a reason other than "closed".
func TestSweep_OneRegionFailsAndOneIsEmpty(t *testing.T) {
	world, _, provider := newFleet(t, 8, east+"/a")
	world.SetScopedFault(fleettest.Scope{Region: eu, Operation: fleettest.EKSListClusters}, fleettest.Fault{Code: "InternalFailure", Status: 500})
	var fetcher fleet.Fetcher
	res := fetcher.Sweep(testContext(t), provider, fleet.Regions{Names: []string{west, eu}, Default: true}, fleet.SweepOptions{})

	if diff := cmp.Diff([]string{west}, res.Answered); diff != "" {
		t.Errorf("Answered (-want +got):\n%s", diff)
	}
	if len(res.Snapshots) != 0 {
		t.Errorf("Snapshots = %v, want none", names(res.Snapshots))
	}
	want := []string{"Region eu-west-1 ServiceError eks:ListClusters"}
	if diff := cmp.Diff(want, failureKeys(res.Failures)); diff != "" {
		t.Errorf("Failures (-want +got):\n%s", diff)
	}
}

func TestSweep_NoRegionAnswers(t *testing.T) {
	world, _, provider := newFleet(t, 8, east+"/a")
	world.SetFault(fleettest.EKSListClusters, fleettest.Fault{Deny: true})
	var fetcher fleet.Fetcher
	res := fetcher.Sweep(testContext(t), provider, fleet.Regions{Names: []string{east, eu}}, fleet.SweepOptions{})
	if len(res.Answered) != 0 || len(res.Snapshots) != 0 {
		t.Errorf("Answered = %v, Snapshots = %v, want none", res.Answered, names(res.Snapshots))
	}
	want := []string{
		"Region us-east-1 AccessDenied eks:ListClusters",
		"Region eu-west-1 AccessDenied eks:ListClusters",
	}
	if diff := cmp.Diff(want, failureKeys(res.Failures)); diff != "" {
		t.Errorf("Failures (-want +got):\n%s", diff)
	}
}

// A cluster that cannot be described stays a row, with its failure.
func TestSweep_ClusterThatFailsIsARow(t *testing.T) {
	world, _, provider := newFleet(t, 8, east+"/a", east+"/b")
	world.SetScopedFault(fleettest.Scope{Operation: fleettest.EKSDescribeCluster, Cluster: "b"}, fleettest.Fault{Deny: true})
	var fetcher fleet.Fetcher
	res := fetcher.Sweep(testContext(t), provider, fleet.Regions{Names: []string{east}}, fleet.SweepOptions{})

	if diff := cmp.Diff([]string{east}, res.Answered); diff != "" {
		t.Errorf("Answered (-want +got):\n%s", diff)
	}
	if len(res.Snapshots) != 2 {
		t.Fatalf("Snapshots = %v, want 2", names(res.Snapshots))
	}
	b := res.Snapshots[1]
	if b.Have.Cluster != fleet.Failed || !b.Incomplete() {
		t.Errorf("Have.Cluster = %v, Incomplete = %v, want Failed and incomplete", b.Have.Cluster, b.Incomplete())
	}
	want := []apidoc.Failure{{
		Kind: apidoc.FailureCluster, Name: "b", Region: east, Operation: "eks:DescribeCluster",
		Reason: apidoc.ReasonAccessDenied,
	}}
	if diff := cmp.Diff(want, b.Failures, cmpopts.IgnoreFields(apidoc.Failure{}, "Error", "AWSErrorCode")); diff != "" {
		t.Errorf("Failures (-want +got):\n%s", diff)
	}
	if doc := b.ClusterDoc(); !doc.Incomplete || doc.Status != "" || doc.Network != nil {
		t.Errorf("document of the failed row = %+v, want only the name, the region, and incomplete", doc)
	}
}

// When the run is cut short, the rows that were done stay complete, the
// row in work fails, and the rows that never started are marked so. The
// sweep works on as many clusters at once as the request limit, in name
// order, so the three groups follow one another. The limit is 1 here, which
// makes the groups exact.
func TestSweep_CancelKeepsFinishedRowsAndMarksTheRest(t *testing.T) {
	const done = 2 // the clusters that are read before the run stops
	clusters := make([]string, 10)
	for i := range clusters {
		clusters[i] = fmt.Sprintf("%s/c%02d", east, i)
	}
	_, srv, provider := newFleet(t, 1, clusters...)

	ctx, cancel := context.WithCancel(testContext(t))
	defer cancel()
	var describes atomic.Int32
	srv.OnRequest = func(hctx context.Context, r awsxtest.Request) {
		if r.Operation != fleettest.EKSDescribeCluster || describes.Add(1) <= done {
			return
		}
		// The clusters before this one were answered. Stop the run while
		// this one is at the server, and hold it until it gives up.
		cancel()
		<-hctx.Done()
	}

	var fetcher fleet.Fetcher
	res := fetcher.Sweep(ctx, provider, fleet.Regions{Names: []string{east}}, fleet.SweepOptions{})
	var states []fleet.State
	for _, snap := range res.Snapshots {
		states = append(states, snap.Have.Cluster)
	}
	want := []fleet.State{fleet.Collected, fleet.Collected, fleet.Failed}
	for len(want) < len(clusters) {
		want = append(want, fleet.NotAttempted)
	}
	if diff := cmp.Diff(want, states); diff != "" {
		t.Fatalf("states of the rows (-want +got):\n%s", diff)
	}
	if f := res.Snapshots[done].Failures; len(f) != 1 || f[0].Reason != apidoc.ReasonInterrupted {
		t.Errorf("failures of the row in work = %+v, want one Interrupted failure", f)
	}
	last := res.Snapshots[len(clusters)-1]
	if len(last.Failures) != 1 || last.Failures[0].Reason != apidoc.ReasonNotAttempted || last.Failures[0].Region != east {
		t.Errorf("failures of the last row = %+v, want one NotAttempted failure in %s", last.Failures, east)
	}
}

// A context that ended before the sweep leaves every region not attempted.
func TestSweep_CancelledBeforeItStarts(t *testing.T) {
	_, srv, provider := newFleet(t, 8, east+"/a")
	ctx, cancel := context.WithCancel(testContext(t))
	cancel()
	var fetcher fleet.Fetcher
	res := fetcher.Sweep(ctx, provider, fleet.Regions{Names: []string{east, eu}}, fleet.SweepOptions{})
	want := []string{"Region us-east-1 NotAttempted ", "Region eu-west-1 NotAttempted "}
	if diff := cmp.Diff(want, failureKeys(res.Failures)); diff != "" {
		t.Errorf("Failures (-want +got):\n%s", diff)
	}
	if n := len(srv.Requests()); n != 0 {
		t.Errorf("requests = %d, want none", n)
	}
}

func TestFetch(t *testing.T) {
	_, _, provider := newFleet(t, 8, east+"/a")
	now := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	fetcher := fleet.Fetcher{Now: func() time.Time { return now }}

	snap, err := fetcher.Fetch(testContext(t), provider.Clients(east), "a", fleet.Need{})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if snap.Have.Cluster != fleet.Collected || snap.Incomplete() || !snap.FetchedAt.Equal(now) {
		t.Errorf("snapshot = %+v", snap)
	}

	snap, err = fetcher.Fetch(testContext(t), provider.Clients(east), "missing", fleet.Need{})
	if !awsx.IsNotFound(err) {
		t.Fatalf("Fetch of a missing cluster: error = %v, want not found", err)
	}
	if snap == nil || snap.Cluster.Name != "missing" || snap.Cluster.Region != east || snap.Have.Cluster != fleet.Failed {
		t.Fatalf("snapshot of a missing cluster = %+v, want the row with its name and region", snap)
	}
	if len(snap.Failures) != 1 || snap.Failures[0].Reason != apidoc.ReasonNotFound {
		t.Errorf("Failures = %+v, want one NotFound failure", snap.Failures)
	}
}

// Every class of an AWS error is a reason that a document can print.
func TestErrorClassesAreReasons(t *testing.T) {
	reasons := map[string]bool{}
	for _, r := range apidoc.Reasons() {
		reasons[string(r)] = true
	}
	for _, c := range awsx.ErrorClasses() {
		if !reasons[string(c)] {
			t.Errorf("the error class %s is not a failure reason", c)
		}
	}
}

// addonWorld returns a fleet with one cluster that has three add-ons.
func addonWorld(t *testing.T) (*fleettest.World, *awsxtest.Server, *awsx.Provider) {
	t.Helper()
	world, srv, provider := newFleet(t, 8, east+"/prod")
	world.Edit(func(s *fleettest.State) {
		s.Clusters[0].Addons = []*fleettest.Addon{
			{Name: "vpc-cni", Version: "v1.19.0-eksbuild.1"},
			{Name: "coredns", Version: "v1.11.3-eksbuild.2", Status: fleettest.StatusDegraded,
				HealthIssues: []fleettest.Issue{{Code: "InsufficientNumberOfReplicas", Message: "no replicas"}}},
			{Name: "kube-proxy", Version: "v1.33.0-eksbuild.2", Status: fleettest.StatusUpdating},
		}
	})
	return world, srv, provider
}

func TestFetch_Addons(t *testing.T) {
	_, srv, provider := addonWorld(t)
	var fetcher fleet.Fetcher
	snap, err := fetcher.Fetch(testContext(t), provider.Clients(east), "prod", fleet.Need{Addons: true})
	if err != nil {
		t.Fatal(err)
	}
	if snap.Have.Addons != fleet.Collected || snap.Incomplete() {
		t.Errorf("Have.Addons = %v, failures = %v", snap.Have.Addons, snap.Failures)
	}
	type row struct {
		Name, Version, Status string
		Health                apidoc.AddonHealth
	}
	var got []row
	for _, a := range snap.Addons {
		got = append(got, row{a.Name, a.Version, a.Status, a.Health()})
	}
	want := []row{
		{"coredns", "v1.11.3-eksbuild.2", "DEGRADED", apidoc.AddonFailed},
		{"kube-proxy", "v1.33.0-eksbuild.2", "UPDATING", apidoc.AddonInProgress},
		{"vpc-cni", "v1.19.0-eksbuild.1", "ACTIVE", apidoc.AddonHealthy},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("add-ons (-want +got):\n%s", diff)
	}
	if n := len(srv.Requests()); n != 5 {
		t.Errorf("requests = %d, want 5: the cluster, the list, and three add-ons", n)
	}
}

// One add-on that cannot be described stays in the list with its name, and
// the list is partial.
func TestFetch_OneAddonFails(t *testing.T) {
	world, _, provider := addonWorld(t)
	world.SetScopedFault(fleettest.Scope{Operation: fleettest.EKSDescribeAddon, Addon: "coredns"}, fleettest.Fault{Deny: true})
	var fetcher fleet.Fetcher
	snap, err := fetcher.Fetch(testContext(t), provider.Clients(east), "prod", fleet.Need{Addons: true})
	if err != nil {
		t.Fatal(err)
	}
	if snap.Have.Addons != fleet.Partial || len(snap.Addons) != 3 || snap.Addons[0].State != fleet.Failed || snap.Addons[1].State != fleet.Collected {
		t.Errorf("Have.Addons = %v, add-ons = %+v", snap.Have.Addons, snap.Addons)
	}
	want := []apidoc.Failure{{
		Kind: apidoc.FailureAddon, Name: "coredns", Cluster: "prod", Region: east,
		Operation: "eks:DescribeAddon", Reason: apidoc.ReasonAccessDenied,
	}}
	if diff := cmp.Diff(want, snap.Failures, cmpopts.IgnoreFields(apidoc.Failure{}, "Error", "AWSErrorCode")); diff != "" {
		t.Errorf("Failures (-want +got):\n%s", diff)
	}
	doc := snap.ClusterDoc()
	if !doc.Incomplete || len(doc.Addons) != 3 || !doc.Addons[0].Incomplete || doc.Addons[0].Status != "" || doc.Addons[1].Health != apidoc.AddonInProgress {
		t.Errorf("document = %+v", doc.Addons)
	}
}

// The list call fails: no add-on is known, and the failure names the
// cluster.
func TestFetch_AddonListFails(t *testing.T) {
	world, _, provider := addonWorld(t)
	world.SetFault(fleettest.EKSListAddons, fleettest.Fault{Deny: true})
	var fetcher fleet.Fetcher
	snap, err := fetcher.Fetch(testContext(t), provider.Clients(east), "prod", fleet.Need{Addons: true})
	if err != nil {
		t.Fatal(err)
	}
	if snap.Have.Addons != fleet.Failed || len(snap.Addons) != 0 {
		t.Errorf("Have.Addons = %v, add-ons = %+v", snap.Have.Addons, snap.Addons)
	}
	if diff := cmp.Diff([]string{"Cluster prod AccessDenied eks:ListAddons"}, failureKeys(snap.Failures)); diff != "" {
		t.Errorf("Failures (-want +got):\n%s", diff)
	}
	if doc := snap.ClusterDoc(); doc.Addons != nil || !doc.Incomplete {
		t.Errorf("document: addons = %v, incomplete = %v, want no addons key and incomplete", doc.Addons, doc.Incomplete)
	}
}

// Named add-ons are described without a list call, and one that does not
// exist is a failure of that add-on.
func TestFetch_OnlyAddons(t *testing.T) {
	_, srv, provider := addonWorld(t)
	var fetcher fleet.Fetcher
	snap, err := fetcher.Fetch(testContext(t), provider.Clients(east), "prod", fleet.Need{OnlyAddons: []string{"vpc-cni", "nope"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range srv.Requests() {
		if r.Operation == fleettest.EKSListAddons {
			t.Error("a list call for add-ons that were named")
		}
	}
	if diff := cmp.Diff([]string{"Addon nope NotFound eks:DescribeAddon"}, failureKeys(snap.Failures)); diff != "" {
		t.Errorf("Failures (-want +got):\n%s", diff)
	}
}

// A cluster that does not exist is one failure, not one for each call.
func TestFetch_MissingClusterIsOneFailure(t *testing.T) {
	_, _, provider := addonWorld(t)
	var fetcher fleet.Fetcher
	snap, err := fetcher.Fetch(testContext(t), provider.Clients(east), "nope", fleet.Need{Addons: true})
	if !awsx.IsNotFound(err) {
		t.Fatalf("error = %v, want not found", err)
	}
	if diff := cmp.Diff([]string{"Cluster nope NotFound eks:DescribeCluster"}, failureKeys(snap.Failures)); diff != "" {
		t.Errorf("Failures (-want +got):\n%s", diff)
	}
}

// The request limit counts requests, not clusters: a sweep whose clusters
// make several calls each has as many requests in flight as the limit, and
// not that many for each cluster.
func TestSweep_RequestLimitCountsRequests(t *testing.T) {
	const limit = 4
	var clusters []string
	for i := range 12 {
		clusters = append(clusters, fmt.Sprintf("%s/c%02d", east, i))
	}
	world, srv, provider := newFleet(t, limit, clusters...)
	world.Edit(func(s *fleettest.State) {
		for _, c := range s.Clusters {
			c.Addons = []*fleettest.Addon{{Name: "a", Version: "v1"}, {Name: "b", Version: "v1"}, {Name: "c", Version: "v1"}}
		}
	})
	var (
		mu       sync.Mutex
		inFlight int
	)
	reached := make(chan struct{})
	srv.OnRequest = func(ctx context.Context, r awsxtest.Request) {
		if r.Operation == fleettest.EKSListClusters {
			return
		}
		mu.Lock()
		inFlight++
		if inFlight == limit {
			close(reached)
		}
		mu.Unlock()
		// Hold the calls until the limit is reached. A lower real limit
		// would stop the sweep here.
		select {
		case <-reached:
		case <-ctx.Done():
		}
	}
	var fetcher fleet.Fetcher
	res := fetcher.Sweep(testContext(t), provider, fleet.Regions{Names: []string{east}}, fleet.SweepOptions{Need: fleet.Need{Addons: true}})
	if len(res.Snapshots) != 12 || len(res.Failures) != 0 {
		t.Fatalf("snapshots = %d, failures = %v", len(res.Snapshots), res.Failures)
	}
	// Each cluster makes five calls, two of them at once at first. With
	// four clusters in work, a limit on clusters alone would let eight
	// requests through.
	if got := srv.PeakConcurrency("", east); got != limit {
		t.Errorf("peak requests in flight = %d, want %d", got, limit)
	}
}
