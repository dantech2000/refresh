package sim

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dantech2000/refresh/internal/tui/state"
)

// advanceUntil steps w until cond holds or limit passes, and fails the test
// on the limit.
func advanceUntil(t *testing.T, w *World, limit time.Duration, cond func(state.State) bool) state.State {
	t.Helper()
	for waited := time.Duration(0); waited <= limit; waited += 5 * time.Second {
		st := w.Snapshot()
		if cond(st) {
			return st
		}
		w.Advance(5 * time.Second)
	}
	t.Fatalf("condition not met within %s of simulated time", limit)
	return state.State{}
}

func findCluster(st state.State, name string) state.Cluster {
	for _, c := range st.Clusters {
		if c.Name == name {
			return c
		}
	}
	return state.Cluster{}
}

func findNG(c state.Cluster, name string) state.Nodegroup {
	for _, ng := range c.Nodegroups {
		if ng.Name == name {
			return ng
		}
	}
	return state.Nodegroup{}
}

func lastRoll(st state.State) state.Roll { return st.Rolls[len(st.Rolls)-1] }

func TestRollReplacesEveryNode(t *testing.T) {
	w := New(Options{Seed: 1})
	plan, err := w.Plan(t.Context(), state.Action{Kind: state.ActionRoll, Cluster: "prod-api", Nodegroup: "ng-general"})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Blocked != "" {
		t.Fatalf("plan blocked: %s", plan.Blocked)
	}
	if err := w.Start(t.Context(), plan.Action); err != nil {
		t.Fatal(err)
	}
	st := w.Snapshot()
	if got := findCluster(st, "prod-api").Busy; got != "rolling ng-general" {
		t.Fatalf("Busy = %q, want rolling ng-general", got)
	}
	// A second roll on a busy cluster is blocked.
	if err := w.Start(t.Context(), state.Action{Kind: state.ActionRoll, Cluster: "prod-api", Nodegroup: "ng-system"}); err == nil {
		t.Fatal("second roll on a busy cluster started")
	}

	sawDraining := false
	st = advanceUntil(t, w, 40*time.Minute, func(s state.State) bool {
		r := lastRoll(s)
		if r.Snapshot.Draining > 0 && len(r.Pods) == 1 {
			sawDraining = true
		}
		return !r.Running()
	})
	if !sawDraining {
		t.Error("never saw a draining node with its pod list")
	}
	r := lastRoll(st)
	if r.Replaced() != 6 || r.Snapshot.Total != 6 || r.Snapshot.ReadyTarget != 6 {
		t.Fatalf("replaced %d, total %d, ready on target %d; want 6/6/6", r.Replaced(), r.Snapshot.Total, r.Snapshot.ReadyTarget)
	}
	ng := findNG(findCluster(st, "prod-api"), "ng-general")
	if ng.AMI != latestAMI("1.31") || ng.Status != "ACTIVE" || ng.NeedsPatch("1.31") {
		t.Fatalf("nodegroup after roll = %+v", ng)
	}
	if findCluster(st, "prod-api").Busy != "" {
		t.Fatal("cluster still busy after the roll")
	}

	counts := map[string]int{}
	var kube, aws, pdb int
	for _, e := range r.Events {
		switch e.Source {
		case state.SourceRoll:
			if e.Subject == "pdb" {
				pdb++
			} else {
				counts[e.Text]++
			}
		case state.SourceKube:
			kube++
		case state.SourceAWS:
			aws++
		}
	}
	for _, kind := range []string{"joining", "online", "draining", "terminated"} {
		if counts[kind] != 6 {
			t.Errorf("%s events = %d, want 6 (%v)", kind, counts[kind], counts)
		}
	}
	if kube == 0 || aws == 0 {
		t.Errorf("kube events %d, aws calls %d; want both > 0", kube, aws)
	}
	if pdb == 0 {
		t.Error("prod-api has a tight PDB, but no pdb event was logged")
	}
}

func TestUpgradeBlockedByReadiness(t *testing.T) {
	w := New(Options{Seed: 1})
	a := state.Action{Kind: state.ActionUpgrade, Cluster: "prod-api"}
	plan, err := w.Plan(t.Context(), a)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(plan.Blocked, "deprecated APIs") {
		t.Fatalf("Blocked = %q, want the deprecated API blocker", plan.Blocked)
	}
	if err := w.Start(t.Context(), a); err == nil || !strings.Contains(err.Error(), "blocked") {
		t.Fatalf("Start = %v, want a blocked error", err)
	}
	if len(w.Snapshot().Upgrades) != 0 {
		t.Fatal("a blocked upgrade started")
	}
}

func TestUpgradeRunsEveryPhase(t *testing.T) {
	w := New(Options{Seed: 2})
	if err := w.Start(t.Context(), state.Action{Kind: state.ActionUpgrade, Cluster: "prod-batch"}); err != nil {
		t.Fatal(err)
	}
	st := advanceUntil(t, w, 90*time.Minute, func(s state.State) bool { return !s.Upgrades[0].Running() })
	u := st.Upgrades[0]
	if u.Failed != "" || u.Stopped {
		t.Fatalf("upgrade ended with failed=%q stopped=%v", u.Failed, u.Stopped)
	}
	for _, p := range u.Phases {
		if p.Status != state.PhaseDone {
			t.Errorf("phase %s status %d, want done", p.Name, p.Status)
		}
	}
	if got := u.Progress(); got != 1 {
		t.Errorf("Progress = %v, want 1", got)
	}
	c := findCluster(st, "prod-batch")
	if c.Version != "1.33" || c.Busy != "" {
		t.Fatalf("cluster after upgrade: version %s busy %q", c.Version, c.Busy)
	}
	if len(c.StaleNodegroups()) != 0 || len(c.StaleAddons()) != 0 {
		t.Fatalf("stale after upgrade: nodegroups %v add-ons %v", c.StaleNodegroups(), c.StaleAddons())
	}
	if rs := st.Readiness["prod-batch"]; rs.Running || len(rs.Checks) == 0 {
		t.Fatalf("pre-flight readiness = %+v", rs)
	}
}

func TestStopAfterCurrentNodegroup(t *testing.T) {
	w := New(Options{Seed: 3})
	if err := w.Start(t.Context(), state.Action{Kind: state.ActionUpgrade, Cluster: "prod-batch"}); err != nil {
		t.Fatal(err)
	}
	advanceUntil(t, w, time.Hour, func(s state.State) bool { return s.Upgrades[0].Current() == phaseNodegroups })
	if err := w.StopAfterCurrent(t.Context(), "prod-batch"); err != nil {
		t.Fatal(err)
	}
	st := advanceUntil(t, w, time.Hour, func(s state.State) bool { return !s.Upgrades[0].Running() })
	u := st.Upgrades[0]
	if !u.Stopped {
		t.Fatalf("upgrade not stopped: %+v", u.Phases[phaseNodegroups])
	}
	items := u.Phases[phaseNodegroups].Items
	if items[0].Status != state.PhaseDone || items[1].Status == state.PhaseDone {
		t.Fatalf("items after stop = %+v; want the first done and the second not", items)
	}
	if u.Phases[phaseNodegroups].Status != state.PhaseStopped {
		t.Fatalf("phase status = %d, want stopped", u.Phases[phaseNodegroups].Status)
	}
	if findCluster(st, "prod-batch").Busy != "" {
		t.Fatal("cluster still busy after the stop")
	}
}

func TestPauseHoldsBeforeNextPhase(t *testing.T) {
	w := New(Options{Seed: 4})
	if err := w.Start(t.Context(), state.Action{Kind: state.ActionUpgrade, Cluster: "prod-batch"}); err != nil {
		t.Fatal(err)
	}
	if err := w.TogglePause(t.Context(), "prod-batch"); err != nil {
		t.Fatal(err)
	}
	w.Advance(10 * time.Minute)
	if cur := w.Snapshot().Upgrades[0].Current(); cur != -1 {
		t.Fatalf("a phase is running while paused: %d", cur)
	}
	if err := w.TogglePause(t.Context(), "prod-batch"); err != nil {
		t.Fatal(err)
	}
	w.Advance(2 * time.Second)
	if cur := w.Snapshot().Upgrades[0].Current(); cur != phasePreflight {
		t.Fatalf("current phase after resume = %d, want pre-flight", cur)
	}
	if err := w.TogglePause(t.Context(), "stage-api"); err == nil {
		t.Fatal("pause on a cluster with no upgrade succeeded")
	}
}

func TestReadinessStreamsResults(t *testing.T) {
	w := New(Options{Seed: 5})
	if err := w.RunReadiness(t.Context(), "prod-api"); err != nil {
		t.Fatal(err)
	}
	r := w.Snapshot().Readiness["prod-api"]
	if !r.Running || r.Pending() != len(r.Checks) || r.From != "1.31" || r.To != "1.32" {
		t.Fatalf("fresh run = %+v", r)
	}
	w.Advance(2 * time.Second)
	r = w.Snapshot().Readiness["prod-api"]
	if r.Pending() == len(r.Checks) || r.Pending() == 0 {
		t.Fatalf("after 2s pending = %d of %d; want some done, some not", r.Pending(), len(r.Checks))
	}
	st := advanceUntil(t, w, time.Minute, func(s state.State) bool { return !s.Readiness["prod-api"].Running })
	r = st.Readiness["prod-api"]
	if r.Blockers() != 1 || r.Warnings() == 0 || len(r.Log) == 0 {
		t.Fatalf("blockers %d warnings %d log %d", r.Blockers(), r.Warnings(), len(r.Log))
	}
	if err := w.RunReadiness(t.Context(), "nope"); err == nil {
		t.Fatal("readiness for an unknown cluster succeeded")
	}
}

func TestAddonUpdate(t *testing.T) {
	w := New(Options{Seed: 6})
	a := state.Action{Kind: state.ActionAddons, Cluster: "prod-api"}
	plan, err := w.Plan(t.Context(), a)
	if err != nil || plan.Blocked != "" || len(plan.Changes) != 2 {
		t.Fatalf("plan = %+v, err %v", plan, err)
	}
	if err := w.Start(t.Context(), a); err != nil {
		t.Fatal(err)
	}
	st := advanceUntil(t, w, 10*time.Minute, func(s state.State) bool { return findCluster(s, "prod-api").Busy == "" })
	if stale := findCluster(st, "prod-api").StaleAddons(); len(stale) != 0 {
		t.Fatalf("stale add-ons after update: %v", stale)
	}
	plan, _ = w.Plan(t.Context(), a)
	if plan.Blocked == "" {
		t.Fatal("a second add-on update is not blocked")
	}
}

func TestWarmupShowsAnUpgradeInFlight(t *testing.T) {
	w := New(Options{Seed: 1, Warmup: 19 * time.Minute})
	st := w.Snapshot()
	if len(st.Upgrades) != 1 || !st.Upgrades[0].Running() {
		t.Fatalf("upgrades after warmup = %+v", st.Upgrades)
	}
	if len(st.Feed) == 0 || len(st.Log) == 0 {
		t.Fatal("warmup left the feed or the log empty")
	}
	if findCluster(st, "prod-eu").Busy == "" {
		t.Fatal("prod-eu is not busy during its upgrade")
	}
}

func TestDeterministic(t *testing.T) {
	run := func() state.State {
		w := New(Options{Seed: 42, Warmup: 5 * time.Minute})
		_ = w.Start(t.Context(), state.Action{Kind: state.ActionRoll, Cluster: "stage-data", Nodegroup: "ng-stream"})
		_ = w.RunReadiness(t.Context(), "dev-sandbox")
		w.Advance(7 * time.Minute)
		return w.Snapshot()
	}
	if a, b := run(), run(); !reflect.DeepEqual(a, b) {
		t.Fatal("two runs with the same seed and calls differ")
	}
}

func TestRunAdvancesPerTickAndStopsWithContext(t *testing.T) {
	w := New(Options{Seed: 1})
	start := w.Now()
	ctx, cancel := context.WithCancel(t.Context())
	ticks := make(chan time.Time)
	done := make(chan struct{})
	go func() {
		w.run(ctx, ticks, 100*time.Millisecond, 20)
		close(done)
	}()
	// Each send is taken only once the previous Advance returned, so after
	// the third send the first two ticks (2 × 100ms × 20 = 4s) are applied.
	for range 3 {
		ticks <- time.Time{}
		_ = w.Snapshot() // safe while run advances the world
	}
	if got := w.Now().Sub(start); got < 4*time.Second {
		t.Fatalf("clock advanced %s after two ticks, want at least 4s", got)
	}
	cancel()
	<-done
}

func TestSupportWindows(t *testing.T) {
	st := New(Options{}).Snapshot()
	if c := findCluster(st, "dev-sandbox"); !c.ExtendedSupport {
		t.Error("dev-sandbox (three minors behind) is not in extended support")
	}
	if c := findCluster(st, "stage-api"); c.ExtendedSupport || c.Behind() != 0 {
		t.Errorf("stage-api = %+v", c)
	}
	if lvl, _ := findCluster(st, "stage-api").Health(); lvl != state.LevelOK {
		t.Errorf("stage-api health = %d, want OK", lvl)
	}
}

func TestConcurrentStartsStartOneRoll(t *testing.T) {
	w := New(Options{Seed: 8})
	a := state.Action{Kind: state.ActionRoll, Cluster: "stage-data", Nodegroup: "ng-stream"}
	const n = 32
	errs := make(chan error, n)
	var wg sync.WaitGroup
	for range n {
		wg.Go(func() { errs <- w.Start(t.Context(), a) })
	}
	wg.Wait()
	close(errs)
	ok := 0
	for err := range errs {
		if err == nil {
			ok++
		}
	}
	if ok != 1 || len(w.Snapshot().Rolls) != 1 {
		t.Fatalf("%d starts succeeded and %d rolls exist; want exactly one", ok, len(w.Snapshot().Rolls))
	}
}

func TestSnapshotSharesNoMemory(t *testing.T) {
	w := New(Options{Seed: 9})
	if err := w.RunReadiness(t.Context(), "prod-api"); err != nil {
		t.Fatal(err)
	}
	w.Advance(time.Minute)
	first := w.Snapshot()
	var dep *state.Check
	r := first.Readiness["prod-api"]
	for i := range r.Checks {
		if r.Checks[i].Table != nil {
			dep = &r.Checks[i]
		}
	}
	if dep == nil {
		t.Fatal("no check with a table")
	}
	dep.Table.Header[0] = "CHANGED"
	dep.Table.Rows[0][0] = "CHANGED"
	dep.Detail[0] = "CHANGED"
	dep.Fix[0] = "CHANGED"
	for _, c := range w.Snapshot().Readiness["prod-api"].Checks {
		if c.Table == nil {
			continue
		}
		if c.Table.Header[0] == "CHANGED" || c.Table.Rows[0][0] == "CHANGED" || c.Detail[0] == "CHANGED" || c.Fix[0] == "CHANGED" {
			t.Fatal("changing a snapshot changed the world")
		}
	}
}

func TestEventSeqIsIncreasingAndMatchesState(t *testing.T) {
	w := New(Options{Seed: 10, Warmup: 3 * time.Minute})
	st := w.Snapshot()
	var maxSeq uint64
	check := func(name string, evs []state.Event) {
		var prev uint64
		for _, e := range evs {
			if e.Seq == 0 || e.Seq <= prev {
				t.Fatalf("%s: seq %d after %d", name, e.Seq, prev)
			}
			prev = e.Seq
			maxSeq = max(maxSeq, e.Seq)
		}
	}
	check("feed", st.Feed)
	check("log", st.Log)
	for _, u := range st.Upgrades {
		check("upgrade", u.Events)
	}
	for _, r := range st.Rolls {
		check("roll", r.Events)
	}
	if maxSeq == 0 || st.Seq < maxSeq {
		t.Fatalf("State.Seq %d below the largest event seq %d", st.Seq, maxSeq)
	}
}

func TestCancelledCallsChangeNothing(t *testing.T) {
	w := New(Options{Seed: 11})
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	a := state.Action{Kind: state.ActionRoll, Cluster: "stage-data", Nodegroup: "ng-stream"}
	if err := w.Start(ctx, a); !errors.Is(err, context.Canceled) {
		t.Fatalf("Start = %v, want context.Canceled", err)
	}
	if err := w.RunReadiness(ctx, "prod-api"); !errors.Is(err, context.Canceled) {
		t.Fatalf("RunReadiness = %v, want context.Canceled", err)
	}
	if _, err := w.State(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("State = %v, want context.Canceled", err)
	}
	st := w.Snapshot()
	if len(st.Rolls) != 0 || len(st.Readiness) != 0 {
		t.Fatal("a cancelled call changed the world")
	}
}

func TestStateHonorsCancelWhileWaitingForTheLock(t *testing.T) {
	w := New(Options{Seed: 12})
	ctx, cancel := context.WithCancel(t.Context())
	w.mu.Lock()
	done := make(chan error, 1)
	go func() {
		_, err := w.State(ctx)
		done <- err
	}()
	cancel()
	w.mu.Unlock()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("State = %v, want context.Canceled", err)
	}
}
