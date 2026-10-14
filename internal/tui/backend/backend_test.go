package backend_test

import (
	"context"
	"errors"
	"math"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dantech2000/refresh/internal/apidoc"
	"github.com/dantech2000/refresh/internal/awsx/awsxtest"
	"github.com/dantech2000/refresh/internal/cmdutil/cmdutiltest"
	"github.com/dantech2000/refresh/internal/fleet"
	"github.com/dantech2000/refresh/internal/fleet/fleettest"
	"github.com/dantech2000/refresh/internal/tui/backend"
	"github.com/dantech2000/refresh/internal/tui/state"
	"github.com/dantech2000/refresh/internal/view"
)

const (
	east = "us-east-1"
	eu   = "eu-west-1"
	ap   = "ap-south-1"
)

func day(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 0, 0, 0, 0, time.UTC) }

// seed is the catalog of every world: the support dates and the add-on
// versions. The clock of the test factory reads 2026-10-01.
func seed(s *fleettest.State) {
	s.ClusterVersions = []fleettest.ClusterVersion{
		{Version: "1.34", EndOfStandardSupport: day(2027, 6, 1), EndOfExtendedSupport: day(2028, 6, 1)},
		{Version: "1.33", EndOfStandardSupport: day(2026, 12, 1), EndOfExtendedSupport: day(2027, 12, 1)},
		{Version: "1.31", EndOfStandardSupport: day(2026, 6, 1), EndOfExtendedSupport: day(2027, 6, 1)},
	}
	for _, v := range []string{"1.31", "1.33", "1.34"} {
		s.AddonVersions = append(s.AddonVersions,
			fleettest.AddonVersion{Addon: "vpc-cni", Version: "v1.20.0-eksbuild.1", ClusterVersions: []string{v}},
			fleettest.AddonVersion{Addon: "coredns", Version: "v1.11.3-eksbuild.2", ClusterVersions: []string{v}},
		)
	}
}

func cluster(name, region, version string) *fleettest.Cluster {
	return &fleettest.Cluster{
		Name: name, Region: region, Version: version,
		Nodegroups: []*fleettest.Nodegroup{{Name: "ng", Version: version}},
		Addons:     []*fleettest.Addon{{Name: "vpc-cni", Version: "v1.20.0-eksbuild.1"}},
	}
}

// rig is a backend over a fake AWS, with a clock the test holds: After
// never fires on its own, so a sweep runs once, and again on Refresh.
type rig struct {
	world  *fleettest.World
	server *awsxtest.Server
	b      *backend.Live
	cancel context.CancelFunc
	done   chan struct{}
	// after is sent to in place of the interval timer.
	after chan time.Time
}

func newRig(t *testing.T, world *fleettest.World, regions fleet.Regions, edit func(o *backend.Options)) *rig {
	t.Helper()
	srv := awsxtest.NewServer(world)
	t.Cleanup(srv.Close)
	f, _, _, _ := cmdutiltest.NewFactory()
	f.Global.MaxConcurrency = 4
	cmdutiltest.UseAWS(f, srv.Config(east))
	provider, err := f.AWS(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	r := &rig{world: world, server: srv, cancel: cancel, done: make(chan struct{}), after: make(chan time.Time)}
	o := backend.Options{
		Regions: regions, Interval: time.Minute, ReadTimeout: 10 * time.Second, Profile: "test",
		Now:   f.Now,
		After: func(time.Duration) <-chan time.Time { return r.after },
	}
	if edit != nil {
		edit(&o)
	}
	r.b = backend.New(ctx, provider, o)
	go func() {
		defer close(r.done)
		r.b.Run()
	}()
	t.Cleanup(func() {
		cancel()
		<-r.done
	})
	return r
}

// wait returns the first state after version that ok accepts.
func (r *rig) wait(t *testing.T, version uint64, ok func(state.State) bool) state.State {
	t.Helper()
	for {
		st, err := r.b.Next(t.Context(), version)
		if err != nil {
			t.Fatal(err)
		}
		if ok(st) {
			return st
		}
		version = st.Version
	}
}

func swept(n int) func(state.State) bool {
	return func(st state.State) bool { return st.Sweep.Count >= n }
}

func feedText(st state.State) string {
	var lines []string
	for _, l := range st.Feed {
		lines = append(lines, l.Cluster.Name+" "+l.Text)
	}
	return strings.Join(lines, "\n")
}

func TestSweepOverRegionsWithOneFailedAndOneSkipped(t *testing.T) {
	world := fleettest.New()
	world.Edit(func(s *fleettest.State) {
		seed(s)
		s.Clusters = append(s.Clusters, cluster("prod", east, "1.33"), cluster("dev", east, "1.34"), cluster("eu-prod", eu, "1.33"), cluster("ap-prod", ap, "1.33"))
	})
	// eu-west-1 fails its cluster list; ap-south-1 is closed to the
	// credentials, which a default sweep skips.
	world.SetScopedFault(fleettest.Scope{Operation: fleettest.EKSListClusters, Region: eu}, fleettest.Fault{Reset: true})
	world.SetScopedFault(fleettest.Scope{Region: ap}, fleettest.Fault{Unauthenticated: true})
	r := newRig(t, world, fleet.Regions{Names: []string{east, eu, ap}, Default: true}, nil)

	// The first version says that a sweep runs; the fleet fills as regions
	// answer.
	first := r.wait(t, 0, func(state.State) bool { return true })
	if first.Sweep.Regions != 3 || !first.Sweep.EndedAt.IsZero() || !first.ReadOnly || first.Profile != "test" {
		t.Fatalf("first state: %+v", first.Sweep)
	}
	st := r.wait(t, first.Version, swept(1))
	if len(st.Clusters) != 2 || st.Clusters[0].Name != "dev" || st.Clusters[1].Name != "prod" {
		t.Fatalf("clusters: %+v", st.Clusters)
	}
	if !slices.Equal(st.Sweep.Answered, []string{east}) || !slices.Equal(st.Sweep.Skipped, []string{ap}) || len(st.Sweep.Failed) != 1 || st.Sweep.Failed[0].Name != eu {
		t.Fatalf("sweep: %+v", st.Sweep)
	}
	if st.Sweep.Error != "" {
		t.Fatalf("a sweep with an answer has an error: %q", st.Sweep.Error)
	}
	for _, c := range st.Clusters {
		if c.Status != view.Healthy || c.Support != apidoc.SupportStandard || c.Incomplete {
			t.Errorf("%s: status %v, support %s, incomplete %v", c.Name, c.Status, c.Support, c.Incomplete)
		}
	}
	feed := feedText(st)
	for _, want := range []string{"region eu-west-1:", "skipped 1 regions closed to the credentials: ap-south-1", "2 clusters: 0 need attention, 0 changing"} {
		if !strings.Contains(feed, want) {
			t.Errorf("feed lacks %q:\n%s", want, feed)
		}
	}
	// The sweep reads what status reads, and no update history.
	for _, req := range r.server.Requests() {
		if req.Operation == fleettest.EKSListUpdates || req.Operation == fleettest.EKSDescribeUpdate {
			t.Fatalf("the sweep read the update history: %s", req.Operation)
		}
	}
	if peak := r.server.PeakConcurrency("eks", east); peak > 4 {
		t.Fatalf("peak concurrency %d, want at most the request limit 4", peak)
	}
}

func TestSecondSweepReportsChanges(t *testing.T) {
	world := fleettest.New()
	world.Edit(func(s *fleettest.State) {
		seed(s)
		s.Clusters = append(s.Clusters, cluster("prod", east, "1.33"), cluster("gone", east, "1.33"))
		s.Clusters[0].Nodegroups[0].Status = "UPDATING"
	})
	r := newRig(t, world, fleet.Regions{Names: []string{east}}, nil)
	st := r.wait(t, 0, swept(1))
	if got := st.Clusters[1].InFlight; !slices.Equal(got, []string{"nodegroup ng UPDATING"}) || st.Clusters[1].Status != view.Progress {
		t.Fatalf("prod in flight: %v, status %v", got, st.Clusters[1].Status)
	}
	world.Edit(func(s *fleettest.State) {
		s.Clusters[0].Version = "1.34"
		s.Clusters[0].Nodegroups[0].Status = "ACTIVE"
		s.Clusters = s.Clusters[:1]
		s.Clusters = append(s.Clusters, cluster("new", east, "1.33"))
	})
	r.b.Refresh()
	st = r.wait(t, st.Version, swept(2))
	feed := feedText(st)
	for _, want := range []string{"prod control plane 1.33 -> 1.34", "prod nodegroup ng UPDATING -> ACTIVE", "gone no longer listed", "new new cluster, 1.33"} {
		if !strings.Contains(feed, want) {
			t.Errorf("feed lacks %q:\n%s", want, feed)
		}
	}
	if len(st.Clusters) != 2 || st.Clusters[0].Name != "new" || st.Clusters[1].Name != "prod" {
		t.Fatalf("clusters after the second sweep: %+v", st.Clusters)
	}
}

func TestNoRegionAnsweredIsExplained(t *testing.T) {
	world := fleettest.New()
	world.Edit(func(s *fleettest.State) { seed(s); s.Clusters = append(s.Clusters, cluster("prod", east, "1.33")) })
	world.SetFault(fleettest.EKSListClusters, fleettest.Fault{Reset: true})
	r := newRig(t, world, fleet.Regions{Names: []string{east}}, func(o *backend.Options) {
		o.SweepError = func(context.Context, *fleet.SweepResult) error {
			return errors.New("no region answered: the network is down")
		}
	})
	st := r.wait(t, 0, swept(1))
	if len(st.Clusters) != 0 || st.Sweep.Error != "no region answered: the network is down" {
		t.Fatalf("clusters %d, error %q", len(st.Clusters), st.Sweep.Error)
	}
}

func TestReadinessRunsInTheBackground(t *testing.T) {
	world := fleettest.New()
	world.Edit(func(s *fleettest.State) {
		seed(s)
		c := cluster("prod", east, "1.33")
		c.Insights = []fleettest.Insight{{Name: "Deprecated APIs", Category: "UPGRADE_READINESS", Status: "WARNING", KubernetesVersion: "1.34"}}
		s.Clusters = append(s.Clusters, c)
	})
	r := newRig(t, world, fleet.Regions{Names: []string{east}}, nil)
	st := r.wait(t, 0, swept(1))
	key := state.Key{Region: east, Name: "prod"}
	if err := r.b.Readiness(t.Context(), state.Key{Region: east, Name: "nope"}); !errors.Is(err, backend.ErrUnknownCluster) {
		t.Fatalf("Readiness of an unknown cluster = %v", err)
	}
	if err := r.b.Readiness(t.Context(), key); err != nil {
		t.Fatal(err)
	}
	done := r.wait(t, st.Version, func(st state.State) bool {
		rd := st.Clusters[0].Readiness
		return rd != nil && !rd.Running()
	})
	rd := done.Clusters[0].Readiness
	if rd.Err != "" || rd.Report == nil || rd.Report.Readiness != apidoc.ReadinessReview {
		t.Fatalf("readiness: err %q, report %+v", rd.Err, rd.Report)
	}
	if !strings.Contains(feedText(done), "prod readiness: review") {
		t.Fatalf("feed:\n%s", feedText(done))
	}
	// The state that showed the run in progress still shows it: a
	// published state is never written to.
	running := r.wait(t, st.Version, func(st state.State) bool { return st.Clusters[0].Readiness != nil })
	if running.Version < done.Version && !running.Clusters[0].Readiness.Running() {
		t.Fatal("the published state of the run in progress was changed")
	}
}

func TestOneReadinessRunPerCluster(t *testing.T) {
	world := fleettest.New()
	world.Edit(func(s *fleettest.State) { seed(s); s.Clusters = append(s.Clusters, cluster("prod", east, "1.33")) })
	// The insights hang until the session ends, so the run never finishes.
	world.SetFault(fleettest.EKSListInsights, fleettest.Fault{Hang: true})
	r := newRig(t, world, fleet.Regions{Names: []string{east}}, nil)
	st := r.wait(t, 0, swept(1))
	key := state.Key{Region: east, Name: "prod"}
	if err := r.b.Readiness(t.Context(), key); err != nil {
		t.Fatal(err)
	}
	r.wait(t, st.Version, func(st state.State) bool { return st.Clusters[0].Readiness != nil })
	if err := r.b.Readiness(t.Context(), key); !errors.Is(err, backend.ErrReadinessRunning) {
		t.Fatalf("a second run = %v, want ErrReadinessRunning", err)
	}
	// The cleanup cancels the session, which ends the hung read and
	// joins the goroutine; goleak checks that.
}

func TestStartIsRefusedWithoutAllowChanges(t *testing.T) {
	world := fleettest.New()
	world.Edit(func(s *fleettest.State) { seed(s); s.Clusters = append(s.Clusters, cluster("prod", east, "1.33")) })
	r := newRig(t, world, fleet.Regions{Names: []string{east}}, nil)
	r.wait(t, 0, swept(1))
	if err := r.b.Start(t.Context(), 1); !errors.Is(err, backend.ErrReadOnly) {
		t.Fatalf("Start without --allow-changes = %v, want ErrReadOnly", err)
	}
	allowed := newRig(t, world, fleet.Regions{Names: []string{east}}, func(o *backend.Options) { o.AllowChanges = true })
	st := allowed.wait(t, 0, swept(1))
	if st.ReadOnly {
		t.Fatal("a backend with --allow-changes reads as read-only")
	}
	if err := allowed.b.Start(t.Context(), 1); !errors.Is(err, backend.ErrNotBuilt) {
		t.Fatalf("Start with --allow-changes = %v, want ErrNotBuilt", err)
	}
	for _, req := range append(r.server.Requests(), allowed.server.Requests()...) {
		if strings.HasPrefix(string(req.Operation), "eks:Update") {
			t.Fatalf("a mutation was sent: %s", req.Operation)
		}
	}
}

func TestNextHonoursCancel(t *testing.T) {
	world := fleettest.New()
	world.Edit(func(s *fleettest.State) { seed(s) })
	r := newRig(t, world, fleet.Regions{Names: []string{east}}, nil)
	st := r.wait(t, 0, swept(1))
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	// The backend may publish a state after the one of the first sweep at
	// any time, and Next returns a state that is there. So the test asks
	// for a version that no state has: only the context can end the call.
	if _, err := r.b.Next(ctx, math.MaxUint64); !errors.Is(err, context.Canceled) {
		t.Fatalf("Next after cancel = %v", err)
	}
	// Versions come in order, each above the one asked for.
	r.b.Refresh()
	next := r.wait(t, st.Version, func(state.State) bool { return true })
	if next.Version <= st.Version {
		t.Fatalf("version %d after %d", next.Version, st.Version)
	}
}

func TestRunStopsWhenTheSessionEnds(t *testing.T) {
	world := fleettest.New()
	world.Edit(func(s *fleettest.State) { seed(s); s.Clusters = append(s.Clusters, cluster("prod", east, "1.33")) })
	// A sweep that hangs ends with the session.
	world.SetFault(fleettest.EKSDescribeCluster, fleettest.Fault{Hang: true})
	r := newRig(t, world, fleet.Regions{Names: []string{east}}, nil)
	r.wait(t, 0, func(state.State) bool { return true })
	r.cancel()
	select {
	case <-r.done:
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return after the session ended")
	}
}
