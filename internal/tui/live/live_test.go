package live

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"

	"github.com/dantech2000/refresh/internal/diag"
	"github.com/dantech2000/refresh/internal/health"
	"github.com/dantech2000/refresh/internal/mocks/fakeaws"
	clustersvc "github.com/dantech2000/refresh/internal/services/cluster"
	statussvc "github.com/dantech2000/refresh/internal/services/status"
	"github.com/dantech2000/refresh/internal/services/upgrade"
	"github.com/dantech2000/refresh/internal/tui/state"
	"github.com/dantech2000/refresh/internal/types"
)

// fleet is a fake status sweep: rows and errors per region.
type fleet struct {
	mu    sync.Mutex
	rows  map[string][]statussvc.ClusterStatus
	errs  map[string]error
	calls atomic.Int64
	// detail records whether every sweep asked for the per-item rows.
	noDetail atomic.Bool
}

func (f *fleet) list(_ context.Context, cfg aws.Config, opts statussvc.ListOptions) ([]statussvc.ClusterStatus, error) {
	f.calls.Add(1)
	if !opts.Detail {
		f.noDetail.Store(true)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.rows[cfg.Region], f.errs[cfg.Region]
}

func (f *fleet) set(region string, rows ...statussvc.ClusterStatus) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rows[region] = rows
}

func newTestBackend(t *testing.T, f *fleet, regions ...string) *Backend {
	t.Helper()
	b := New(aws.Config{Region: regions[0]}, Options{Regions: regions, Context: "prod", Profile: "admin"})
	b.now = func() time.Time { return time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC) }
	b.svc = services{
		listStatuses:  f.list,
		latestVersion: func(context.Context, aws.Config) (string, error) { return "1.33", nil },
		upgradeCheck: func(context.Context, aws.Config, string) (*clustersvc.UpgradeReport, error) {
			return nil, errors.New("no upgrade check in this test")
		},
		buildPlan: func(context.Context, aws.Config, string, string) (*upgrade.Plan, error) {
			return nil, errors.New("no planner in this test")
		},
	}
	return b
}

func standardUntil(y int) *time.Time {
	t := time.Date(y, 1, 1, 0, 0, 0, 0, time.UTC)
	return &t
}

func prodRows() []statussvc.ClusterStatus {
	return []statussvc.ClusterStatus{
		{
			Name: "prod-api", Region: "us-east-1", Version: "1.31",
			Support: statussvc.SupportPosture{Tier: statussvc.SupportStandard, StandardUntil: standardUntil(2026)},
			Nodegroups: []statussvc.NodegroupPosture{
				{Name: "ng-general", Version: "1.31", CurrentAMI: "ami-old", AMIStatus: types.AMIOutdated, DesiredSize: 6, Status: "ACTIVE"},
				{Name: "ng-system", Version: "1.31", CurrentAMI: "ami-new", AMIStatus: types.AMILatest, DesiredSize: 3, Status: "UPDATING"},
			},
			Addons: []statussvc.AddonPosture{
				{Name: "vpc-cni", Version: "v1.18.3", Latest: "v1.19.2", Behind: true, Status: "ACTIVE"},
				{Name: "coredns", Version: "v1.11.4", Latest: "", Status: "ACTIVE"}, // newest unknown
			},
		},
		{Name: "shared", Region: "us-east-1", Version: "1.33", Support: statussvc.SupportPosture{Tier: statussvc.SupportStandard}},
	}
}

func TestSweepBuildsTheFleet(t *testing.T) {
	f := &fleet{rows: map[string][]statussvc.ClusterStatus{
		"us-east-1": prodRows(),
		"eu-west-1": {
			{Name: "shared", Region: "eu-west-1", Version: "1.30", Support: statussvc.SupportPosture{Tier: statussvc.SupportExtended}, Incomplete: true},
		},
	}}
	b := newTestBackend(t, f, "us-east-1", "eu-west-1")
	b.sweep(t.Context())
	st, err := b.State(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if f.noDetail.Load() {
		t.Fatal("the sweep did not ask for per-item detail")
	}
	names := []string{}
	for _, c := range st.Clusters {
		names = append(names, c.Name)
	}
	// Sorted by region, then name; the shared name carries its region.
	if got := strings.Join(names, ","); got != "shared@eu-west-1,prod-api,shared@us-east-1" {
		t.Fatalf("clusters = %s", got)
	}
	var prod, euShared state.Cluster
	for _, c := range st.Clusters {
		switch c.Name {
		case "prod-api":
			prod = c
		case "shared@eu-west-1":
			euShared = c
		}
	}
	if prod.Latest != "1.33" || prod.Behind() != 2 || prod.SupportEnds.Year() != 2026 {
		t.Fatalf("prod = %+v", prod)
	}
	if prod.Busy != "updating ng-system" {
		t.Fatalf("Busy = %q, want the nodegroup EKS reports UPDATING", prod.Busy)
	}
	if stale := prod.StaleNodegroups(); len(stale) != 1 || stale[0].Name != "ng-general" || stale[0].Nodes != 6 {
		t.Fatalf("stale nodegroups = %+v", stale)
	}
	if stale := prod.StaleAddons(); len(stale) != 1 || stale[0].Name != "vpc-cni" {
		t.Fatalf("stale add-ons = %+v; an unknown newest version must not count", stale)
	}
	if !euShared.ExtendedSupport || !euShared.Incomplete {
		t.Fatalf("eu shared = %+v", euShared)
	}
	if st.RegionsAnswered != 2 || st.RegionsTotal != 2 || st.Badge != "READ-ONLY" || st.Context != "prod" || st.Profile != "admin" {
		t.Fatalf("state header = %+v", st)
	}
	if len(st.Feed) == 0 || len(st.Log) == 0 {
		t.Fatal("the first sweep left the feed or the log empty")
	}
}

func TestUnknownNewestVersionNeverReadsAsBehind(t *testing.T) {
	f := &fleet{rows: map[string][]statussvc.ClusterStatus{"us-east-1": prodRows()}}
	b := newTestBackend(t, f, "us-east-1")
	b.svc.latestVersion = func(context.Context, aws.Config) (string, error) { return "", errors.New("AccessDenied") }
	b.sweep(t.Context())
	st, _ := b.State(t.Context())
	for _, c := range st.Clusters {
		if c.Behind() != 0 || c.Latest != "" {
			t.Fatalf("%s reads %d behind (latest %q) with the newest version unknown", c.Name, c.Behind(), c.Latest)
		}
	}
	// An upgrade can still be planned: the planner checks the target.
	b.svc.buildPlan = func(_ context.Context, _ aws.Config, cluster, target string) (*upgrade.Plan, error) {
		return &upgrade.Plan{ClusterName: cluster, CurrentVersion: "1.33", TargetVersion: target}, nil
	}
	p, err := b.Plan(t.Context(), state.Action{Kind: state.ActionUpgrade, Cluster: "shared"})
	if err != nil || !strings.Contains(p.Command, "--to 1.34") || strings.Contains(p.Blocked, "newest version") {
		t.Fatalf("plan with the newest version unknown = %+v, %v", p, err)
	}
	if !strings.Contains(joinText(st.Log), "newest EKS version unknown") {
		t.Fatal("the failed version lookup is not in the log")
	}
}

func joinText(evs []state.Event) string {
	var parts []string
	for _, e := range evs {
		parts = append(parts, e.Subject+" "+e.Text+" "+e.Detail)
	}
	return strings.Join(parts, "\n")
}

func TestRegionFailureIsReportedAndOthersStay(t *testing.T) {
	f := &fleet{
		rows: map[string][]statussvc.ClusterStatus{"us-east-1": prodRows()},
		errs: map[string]error{"eu-west-1": errors.New("AccessDenied: no eks:ListClusters")},
	}
	b := newTestBackend(t, f, "us-east-1", "eu-west-1")
	b.sweep(t.Context())
	st, _ := b.State(t.Context())
	if st.RegionsAnswered != 1 || st.RegionsTotal != 2 || len(st.Clusters) != 2 {
		t.Fatalf("answered %d/%d, %d clusters", st.RegionsAnswered, st.RegionsTotal, len(st.Clusters))
	}
	if !strings.Contains(joinText(st.Feed), "region could not be read") {
		t.Fatal("the failed region is not in the feed")
	}
}

func TestSecondSweepReportsChanges(t *testing.T) {
	f := &fleet{rows: map[string][]statussvc.ClusterStatus{"us-east-1": prodRows()}}
	b := newTestBackend(t, f, "us-east-1")
	b.sweep(t.Context())
	rows := prodRows()
	rows[0].Version = "1.32"
	rows[0].Nodegroups[1].Status = "ACTIVE"
	rows = rows[:1] // "shared" is gone
	f.set("us-east-1", rows...)
	b.sweep(t.Context())
	st, _ := b.State(t.Context())
	text := joinText(st.Feed)
	for _, want := range []string{"control plane 1.31 → 1.32", "ng-system UPDATING → ACTIVE", "cluster no longer listed"} {
		if !strings.Contains(text, want) {
			t.Errorf("feed lacks %q:\n%s", want, text)
		}
	}
}

func TestStateSharesNoMemory(t *testing.T) {
	f := &fleet{rows: map[string][]statussvc.ClusterStatus{"us-east-1": prodRows()}}
	b := newTestBackend(t, f, "us-east-1")
	b.sweep(t.Context())
	st, _ := b.State(t.Context())
	st.Clusters[0].Nodegroups[0].Name = "CHANGED"
	st.Feed[0].Text = "CHANGED"
	again, _ := b.State(t.Context())
	if again.Clusters[0].Nodegroups[0].Name == "CHANGED" || again.Feed[0].Text == "CHANGED" {
		t.Fatal("changing a snapshot changed the backend")
	}
}

func TestStateHonorsCancel(t *testing.T) {
	b := newTestBackend(t, &fleet{rows: map[string][]statussvc.ClusterStatus{}}, "us-east-1")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := b.State(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("State = %v, want context.Canceled", err)
	}
}

func report() *clustersvc.UpgradeReport {
	return &clustersvc.UpgradeReport{
		Cluster: "prod-api",
		Support: &statussvc.SupportPosture{Tier: statussvc.SupportExtended},
		ControlPlane: &health.HealthResult{
			Name: "control plane", Status: health.StatusPass, Message: "healthy",
		},
		Insights: []clustersvc.InsightSummary{
			{ID: "abc123", Name: "Deprecated APIs removed in Kubernetes v1.32", Status: "ERROR", StatusReason: "flowcontrol v1beta3 in use", Description: "EKS found calls to removed APIs."},
			{ID: "def456", Name: "Kubelet version skew", Status: "PASSING"},
		},
		Skew: clustersvc.SkewReport{
			ControlPlaneVersion: "1.31",
			Nodegroups:          []clustersvc.NodegroupSkew{{Name: "ng-old", Version: "1.28", MinorsBehind: 3, Blocking: true}, {Name: "ng-ok", Version: "1.31"}},
			Addons:              []clustersvc.AddonSkew{{Name: "vpc-cni", Installed: "v1.18.3", Latest: "v1.19.2", Behind: true}, {Name: "coredns", Installed: "v1.11.4", Incomplete: true}},
			Findings:            []string{"ng-old is 3 minors behind the control plane"},
		},
		Failures: diag.List{diag.New(diag.KindNodegroup, "ng-gone", diag.ReasonUnknown, "DescribeNodegroup failed")},
	}
}

func TestReadinessRunsInTheBackgroundAndMapsTheReport(t *testing.T) {
	f := &fleet{rows: map[string][]statussvc.ClusterStatus{"us-east-1": prodRows()}}
	b := newTestBackend(t, f, "us-east-1")
	release := make(chan struct{})
	b.svc.upgradeCheck = func(_ context.Context, cfg aws.Config, cluster string) (*clustersvc.UpgradeReport, error) {
		<-release
		if cluster != "prod-api" || cfg.Region != "us-east-1" {
			return nil, errors.New("wrong target")
		}
		return report(), nil
	}
	b.sweep(t.Context())
	if err := b.RunReadiness(t.Context(), "prod-api"); err != nil {
		t.Fatal(err)
	}
	// RunReadiness returned while the check is still running.
	st, _ := b.State(t.Context())
	if r := st.Readiness["prod-api"]; !r.Running || r.From != "1.31" || r.To != "1.32" {
		t.Fatalf("readiness while running = %+v", r)
	}
	close(release)
	b.Close()
	st, _ = b.State(t.Context())
	r := st.Readiness["prod-api"]
	if r.Running {
		t.Fatal("still running after the check returned")
	}
	byName := map[string]state.Check{}
	for _, c := range r.Checks {
		byName[c.Name] = c
	}
	want := map[string]state.CheckStatus{
		"support":      state.CheckWarn,
		"health":       state.CheckPass,
		"version skew": state.CheckPass,
		"Deprecated APIs removed in Kubernetes v1.32": state.CheckFail,
		"Kubelet version skew":                        state.CheckPass,
		"ng-old":                                      state.CheckFail,
		"ng-ok":                                       state.CheckPass,
		"vpc-cni":                                     state.CheckWarn,
		"coredns":                                     state.CheckWarn,
		"ng-gone":                                     state.CheckWarn,
	}
	for name, st := range want {
		if byName[name].Status != st {
			t.Errorf("%s status = %d, want %d", name, byName[name].Status, st)
		}
	}
	fix := strings.Join(byName["Deprecated APIs removed in Kubernetes v1.32"].Fix, " ")
	if !strings.Contains(fix, "refresh --region us-east-1 cluster upgrade-check -c prod-api --id abc123") {
		t.Fatalf("fix = %q", fix)
	}
	if r.Blockers() != 2 || !strings.Contains(joinText(st.Feed), "readiness: 2 blocker(s)") {
		t.Fatalf("blockers = %d, feed:\n%s", r.Blockers(), joinText(st.Feed))
	}
}

func TestReadinessErrorIsAFailedCheck(t *testing.T) {
	f := &fleet{rows: map[string][]statussvc.ClusterStatus{"us-east-1": prodRows()}}
	b := newTestBackend(t, f, "us-east-1")
	b.svc.upgradeCheck = func(context.Context, aws.Config, string) (*clustersvc.UpgradeReport, error) {
		return nil, errors.New("AccessDenied: eks:ListInsights")
	}
	b.sweep(t.Context())
	if err := b.RunReadiness(t.Context(), "prod-api"); err != nil {
		t.Fatal(err)
	}
	b.Close()
	st, _ := b.State(t.Context())
	r := st.Readiness["prod-api"]
	if r.Running || len(r.Checks) != 1 || r.Checks[0].Status != state.CheckFail || !strings.Contains(r.Checks[0].Detail[0], "ListInsights") {
		t.Fatalf("readiness = %+v", r)
	}
	if err := b.RunReadiness(t.Context(), "nope"); err == nil {
		t.Fatal("readiness for a cluster outside the fleet succeeded")
	}
}

func TestPlansAreDryRunsWithTheCLICommand(t *testing.T) {
	f := &fleet{rows: map[string][]statussvc.ClusterStatus{"us-east-1": prodRows()}}
	b := newTestBackend(t, f, "us-east-1")
	b.sweep(t.Context())

	roll, err := b.Plan(t.Context(), state.Action{Kind: state.ActionRoll, Cluster: "prod-api", Nodegroup: "ng-general"})
	if err != nil {
		t.Fatal(err)
	}
	if roll.Command != "refresh --region us-east-1 nodegroup update -c prod-api -n ng-general" || roll.Blocked != ErrReadOnly.Error() {
		t.Fatalf("roll plan = %+v", roll)
	}
	if len(roll.Changes) != 1 || roll.Changes[0].From != "ami-old" {
		t.Fatalf("roll changes = %+v", roll.Changes)
	}
	current, _ := b.Plan(t.Context(), state.Action{Kind: state.ActionRoll, Cluster: "prod-api", Nodegroup: "ng-system"})
	if !strings.Contains(current.Blocked, "already runs the latest AMI") {
		t.Fatalf("current nodegroup plan blocked = %q", current.Blocked)
	}

	addons, _ := b.Plan(t.Context(), state.Action{Kind: state.ActionAddons, Cluster: "prod-api"})
	if len(addons.Changes) != 1 || addons.Command != "refresh --region us-east-1 addon update --all -c prod-api" {
		t.Fatalf("add-on plan = %+v", addons)
	}

	b.svc.buildPlan = func(_ context.Context, _ aws.Config, cluster, target string) (*upgrade.Plan, error) {
		return &upgrade.Plan{
			ClusterName: cluster, CurrentVersion: "1.31", TargetVersion: target,
			Hops: []upgrade.Hop{{From: "1.31", To: target, Steps: []upgrade.Step{
				{Type: upgrade.StepReadiness, Description: "readiness", Status: upgrade.StatusBlocked, Reason: "1 insight in ERROR"},
				{Type: upgrade.StepControlPlane, Description: "control plane to " + target, Status: upgrade.StatusPending},
				{Type: upgrade.StepAddon, Target: "vpc-cni", Description: "vpc-cni to v1.19.2", Status: upgrade.StatusPending},
				{Type: upgrade.StepAddon, Target: "aws-ebs-csi-driver", Description: "managed by Helm", Status: upgrade.StatusManual, Reason: "--skip-addons"},
			}}},
			Notices: []string{"insights were refreshed 3d ago"},
		}, nil
	}
	up, err := b.Plan(t.Context(), state.Action{Kind: state.ActionUpgrade, Cluster: "prod-api"})
	if err != nil {
		t.Fatal(err)
	}
	if up.Command != "refresh --region us-east-1 cluster upgrade -c prod-api --to 1.32" || !strings.Contains(up.Blocked, "1 blocker(s): Readiness") {
		t.Fatalf("upgrade plan = %+v", up)
	}
	if len(up.Gates) != 3 || len(up.Facts) != 3 {
		t.Fatalf("gates %+v facts %+v", up.Gates, up.Facts)
	}

	latest, _ := b.Plan(t.Context(), state.Action{Kind: state.ActionUpgrade, Cluster: "shared"})
	if !strings.Contains(latest.Blocked, "newest version") {
		t.Fatalf("plan on the newest version blocked = %q", latest.Blocked)
	}
}

func TestChangesAreRefused(t *testing.T) {
	b := newTestBackend(t, &fleet{rows: map[string][]statussvc.ClusterStatus{}}, "us-east-1")
	a := state.Action{Kind: state.ActionRoll, Cluster: "prod-api", Nodegroup: "ng-general"}
	for name, err := range map[string]error{
		"Start": b.Start(t.Context(), a),
		"Stop":  b.StopAfterCurrent(t.Context(), "prod-api"),
		"Pause": b.TogglePause(t.Context(), "prod-api"),
	} {
		if !errors.Is(err, ErrReadOnly) {
			t.Errorf("%s = %v, want ErrReadOnly", name, err)
		}
	}
}

func TestRunSweepsOnTicksAndRefresh(t *testing.T) {
	f := &fleet{rows: map[string][]statussvc.ClusterStatus{"us-east-1": prodRows()}}
	b := newTestBackend(t, f, "us-east-1")
	ctx, cancel := context.WithCancel(t.Context())
	ticks := make(chan time.Time)
	done := make(chan struct{})
	go func() {
		b.run(ctx, ticks)
		close(done)
	}()
	// The loop sweeps at once, then waits. A send is taken only after the
	// sweep before it returned, so after it the first sweep is complete.
	ticks <- time.Time{}
	ticks <- time.Time{}
	if n := f.calls.Load(); n < 2 {
		t.Fatalf("%d sweeps after one tick, want at least 2", n)
	}
	before := f.calls.Load()
	b.Refresh()
	ticks <- time.Time{} // taken once the refresh sweep finished
	if n := f.calls.Load(); n < before+1 {
		t.Fatalf("Refresh did not sweep: %d calls, had %d", n, before)
	}
	cancel()
	<-done
}

// TestAgainstFakeAWS runs the real status service, upgrade check, and
// planner over fakeaws. EC2 and SSM are not modeled, so AMI lookups fail
// and the cluster reads as incomplete, as it would without those
// permissions.
func TestAgainstFakeAWS(t *testing.T) {
	srv := fakeaws.New(t,
		&fakeaws.Cluster{
			Name: "prod", Version: "1.32",
			Nodegroups: []*fakeaws.Nodegroup{{Name: "ng-a", Version: "1.32"}},
			Addons:     []*fakeaws.Addon{{Name: "vpc-cni", Version: "v1.18.0", Available: []string{"v1.19.2", "v1.18.0"}}},
			Insights:   []*fakeaws.Insight{{ID: "i-1", Name: "Deprecated APIs", Status: "ERROR"}},
		},
	)
	srv.SetSupportedVersions("1.31", "1.32", "1.33")
	cfg, err := config.LoadDefaultConfig(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	b := New(cfg, Options{SweepTimeout: 30 * time.Second})
	b.sweep(t.Context())
	st, err := b.State(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Clusters) != 1 || st.Clusters[0].Name != "prod" || st.Clusters[0].Version != "1.32" {
		t.Fatalf("clusters = %+v\nlog:\n%s", st.Clusters, joinText(st.Log))
	}
	c := st.Clusters[0]
	if c.Latest != "1.33" || c.Behind() != 1 || len(c.Nodegroups) != 1 || len(c.StaleAddons()) != 1 {
		t.Fatalf("cluster = %+v", c)
	}

	if err := b.RunReadiness(t.Context(), "prod"); err != nil {
		t.Fatal(err)
	}
	b.Close()
	st, _ = b.State(t.Context())
	r := st.Readiness["prod"]
	if r.Running || r.Blockers() == 0 {
		t.Fatalf("readiness = %+v", r)
	}

	p, err := b.Plan(t.Context(), state.Action{Kind: state.ActionUpgrade, Cluster: "prod"})
	if err != nil {
		t.Fatal(err)
	}
	if p.Blocked == "" || !strings.HasPrefix(p.Command, "refresh --region us-east-1 cluster upgrade -c prod --to ") {
		t.Fatalf("upgrade plan = %+v", p)
	}
}

func TestServiceLogsGoToThePaneNotTheTerminal(t *testing.T) {
	b := New(aws.Config{Region: "us-east-1"}, Options{})
	log := b.opts.Logger.With("cluster", "prod")
	log.Info("routine detail") // below warn: dropped
	log.Warn("AMI lookup failed", "nodegroup", "ng-a")
	log.Error("describe failed")
	st, _ := b.State(t.Context())
	text := joinText(st.Log)
	if strings.Contains(text, "routine detail") {
		t.Fatal("an info log reached the pane")
	}
	if !strings.Contains(text, "AMI lookup failed cluster=prod nodegroup=ng-a") || !strings.Contains(text, "describe failed") {
		t.Fatalf("log pane:\n%s", text)
	}
}

func TestNilReportIsAFailedCheck(t *testing.T) {
	f := &fleet{rows: map[string][]statussvc.ClusterStatus{"us-east-1": prodRows()}}
	b := newTestBackend(t, f, "us-east-1")
	b.svc.upgradeCheck = func(context.Context, aws.Config, string) (*clustersvc.UpgradeReport, error) { return nil, nil }
	b.sweep(t.Context())
	if err := b.RunReadiness(t.Context(), "prod-api"); err != nil {
		t.Fatal(err)
	}
	b.Close()
	st, _ := b.State(t.Context())
	if r := st.Readiness["prod-api"]; r.Running || r.Checks[0].Status != state.CheckFail {
		t.Fatalf("readiness = %+v", r)
	}
}
