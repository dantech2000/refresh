package backend_test

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"k8s.io/client-go/kubernetes"

	"github.com/dantech2000/refresh/internal/apidoc"
	"github.com/dantech2000/refresh/internal/change"
	"github.com/dantech2000/refresh/internal/cmdutil/cmdutiltest"
	"github.com/dantech2000/refresh/internal/fleet"
	"github.com/dantech2000/refresh/internal/fleet/fleettest"
	"github.com/dantech2000/refresh/internal/kube"
	"github.com/dantech2000/refresh/internal/kube/kubetest"
	"github.com/dantech2000/refresh/internal/noderoll"
	"github.com/dantech2000/refresh/internal/tui/backend"
	"github.com/dantech2000/refresh/internal/tui/state"
)

const (
	cniOld = "v1.19.0-eksbuild.1"
	cniNew = "v1.20.0-eksbuild.1"
	// oldImage is the AMI of a nodegroup that is behind.
	oldImage = "ami-0123456789abcdef0"
)

var prod = state.Key{Region: east, Name: "prod"}

// changeWorld is a fleet of one cluster, prod, on 1.33 with a stale add-on
// and one nodegroup, web, on an old AMI with two nodes, each with a pod of
// its own and a budget that lets one go. The clock reads the clock of
// the test factory, so the sweep and the changes agree on the time.
func changeWorld(edit func(s *fleettest.State, c *fleettest.Cluster)) *fleettest.World {
	world := fleettest.NewAt(cmdutiltest.Now)
	world.Edit(func(s *fleettest.State) {
		seed(s)
		c := &fleettest.Cluster{
			Name: "prod", Region: east, Version: "1.33",
			Addons: []*fleettest.Addon{{Name: "vpc-cni", Version: cniOld}, {Name: "coredns", Version: "v1.11.3-eksbuild.2"}},
		}
		s.ClusterVersions = append(s.ClusterVersions, fleettest.ClusterVersion{Version: "1.32", EndOfStandardSupport: day(2026, 9, 1), EndOfExtendedSupport: day(2027, 9, 1)})
		s.AddonVersions = append(s.AddonVersions,
			fleettest.AddonVersion{Addon: "vpc-cni", Version: cniOld, ClusterVersions: []string{"1.32", "1.33"}},
			fleettest.AddonVersion{Addon: "vpc-cni", Version: cniNew, ClusterVersions: []string{"1.32"}},
			fleettest.AddonVersion{Addon: "coredns", Version: "v1.11.3-eksbuild.2", ClusterVersions: []string{"1.32"}},
		)
		group := "eks-prod-web"
		c.Nodegroups = append(c.Nodegroups, &fleettest.Nodegroup{Name: "web", Version: "1.33", ReleaseVersion: "1.33.0-20250101", AutoScalingGroups: []string{group}})
		ids := []string{"i-web-1", "i-web-2"}
		s.AutoScalingGroups = append(s.AutoScalingGroups, fleettest.AutoScalingGroup{Name: group, InstanceIDs: ids})
		s.Images = append(s.Images, fleettest.Image{ID: oldImage, Name: oldImage})
		app := map[string]string{"app": "web"}
		for i, id := range ids {
			s.Instances = append(s.Instances, fleettest.Instance{ID: id, ImageID: oldImage, Type: "m5.large", AvailabilityZone: east + "a"})
			node := fmt.Sprintf("web-%d", i+1)
			c.Nodes = append(c.Nodes, &fleettest.Node{Name: node, Nodegroup: "web", ImageID: oldImage, UsedMilliCPU: 400, UsedMemoryMi: 2048})
			c.Pods = append(c.Pods, &fleettest.Pod{Namespace: "default", Name: node, Node: node, Labels: app})
		}
		c.PDBs = append(c.PDBs, fleettest.PDB{Namespace: "default", Name: "web", Selector: app, MinAvailable: 1, DisruptionsAllowed: 1, CurrentHealthy: 2, DesiredHealthy: 1, ExpectedPods: 2})
		c.Deployments = append(c.Deployments, fleettest.Deployment{Namespace: "default", Name: "web", Replicas: 2, Selector: app})
		s.Clusters = append(s.Clusters, c)
		if edit != nil {
			edit(s, c)
		}
	})
	return world
}

// changeRig is a rig with --allow-changes, whose changes run on the clock
// of the world: a pause of the engine moves the world and ends at once.
// The sweep timer stays the test's, so a sweep runs only on Refresh. With
// withKube the Kubernetes API of prod is served and reached through a
// kubeconfig, as a user's would be.
func changeRig(t *testing.T, world *fleettest.World, withKube bool) *rig {
	t.Helper()
	return pacedRig(t, world, withKube, nil)
}

// pacedRig is changeRig with a pacer as the clock, when p is not nil.
func pacedRig(t *testing.T, world *fleettest.World, withKube bool, p *pacer) *rig {
	t.Helper()
	var kubeFn func(kube.Target) (*kube.Client, error)
	if withKube {
		srv := kubetest.NewServer(world, east, "prod")
		t.Cleanup(srv.Close)
		path := filepath.Join(t.TempDir(), "kubeconfig")
		if err := srv.WriteKubeconfig(path); err != nil {
			t.Fatal(err)
		}
		t.Setenv("KUBECONFIG", path)
		client, err := kubernetes.NewForConfig(srv.Config())
		if err != nil {
			t.Fatal(err)
		}
		kubeFn = func(kube.Target) (*kube.Client, error) { return &kube.Client{Clientset: client}, nil }
	}
	return newRig(t, world, fleet.Regions{Names: []string{east}}, func(o *backend.Options) {
		o.AllowChanges = true
		o.Now = world.Now
		o.Deps = change.Deps{
			Fetch: &fleet.Fetcher{Now: world.Now},
			Kube:  kubeFn,
			Now:   world.Now,
			After: func(d time.Duration) <-chan time.Time {
				world.Advance(d)
				done := make(chan time.Time, 1)
				done <- world.Now()
				return done
			},
			// The node observer takes its snapshots as fast as the roll
			// goes: the clock does not move for it.
			Tick: func(time.Duration) <-chan time.Time {
				done := make(chan time.Time, 1)
				done <- world.Now()
				return done
			},
			// No call hangs: the budgets run out on the world's clock.
			AfterFunc: func(time.Duration, func()) func() bool { return func() bool { return true } },
		}
		if p != nil {
			o.Deps.After, o.Deps.Tick = p.After, p.Tick
		}
	})
}

// pacer is the clock of a test that reads the nodes of a roll. The node
// observer runs beside the wait of the roll, and reads the cluster
// through informers that take real time to start, while the wait moves
// the world's clock without waiting for anything; the observer would
// miss the roll. The pacer parks each pause of the engine until the
// observer has taken a snapshot of the world as it is, so every stage of
// the roll is seen, with no timer and no sleep.
type pacer struct {
	world *fleettest.World
	stop  chan struct{}

	mu sync.Mutex
	// after is the release of the parked pause of the engine, or nil;
	// tick the channel of the parked observer, or nil; seen says that an
	// observer has parked at least once.
	after chan struct{}
	tick  chan time.Time
	seen  bool
	// wake is closed and replaced on every park.
	wake chan struct{}
}

func newPacer(t *testing.T, world *fleettest.World) *pacer {
	t.Helper()
	p := &pacer{world: world, stop: make(chan struct{}), wake: make(chan struct{})}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for p.step() {
		}
	}()
	t.Cleanup(func() {
		close(p.stop)
		<-done
	})
	return p
}

func (p *pacer) wakeLocked() {
	close(p.wake)
	p.wake = make(chan struct{})
}

// After is the pause of the engine: it parks until the pacer releases
// it, or the test ends, then moves the world's clock.
func (p *pacer) After(d time.Duration) <-chan time.Time {
	rel := make(chan struct{})
	p.mu.Lock()
	p.after = rel
	p.wakeLocked()
	p.mu.Unlock()
	select {
	case <-rel:
	case <-p.stop:
	}
	p.world.Advance(d)
	out := make(chan time.Time, 1)
	out <- p.world.Now()
	return out
}

// Tick is the pause of the observer: the channel delivers when the
// pacer releases it. The observer selects on it with its context, so an
// observer that ends while parked is not held.
func (p *pacer) Tick(time.Duration) <-chan time.Time {
	ch := make(chan time.Time, 1)
	p.mu.Lock()
	p.tick, p.seen = ch, true
	p.wakeLocked()
	p.mu.Unlock()
	return ch
}

// await waits until ok holds under the lock, and returns false when the
// test ended first.
func (p *pacer) await(ok func() bool) bool {
	for {
		p.mu.Lock()
		if ok() {
			p.mu.Unlock()
			return true
		}
		wake := p.wake
		p.mu.Unlock()
		select {
		case <-wake:
		case <-p.stop:
			return false
		}
	}
}

// step releases one pause of the engine, after the observer, if there is
// one, has snapshotted the world as it is. The first pause waits for the
// observer to attach. An observer that is gone, because the wait it ran
// beside ended, parks no more; a pause then goes on at once.
func (p *pacer) step() bool {
	if !p.await(func() bool { return p.after != nil }) {
		return false
	}
	if !p.seen && !p.await(func() bool { return p.tick != nil }) {
		return false
	}
	p.mu.Lock()
	tick := p.tick
	p.tick = nil
	p.mu.Unlock()
	if tick != nil {
		tick <- p.world.Now()
		// The observer snapshots the world and parks again, unless its
		// wait ended meanwhile; both wake the pacer.
		if !p.await(func() bool { return p.tick != nil || p.after == nil }) {
			return false
		}
	}
	p.mu.Lock()
	rel := p.after
	p.after = nil
	p.mu.Unlock()
	close(rel)
	return true
}

// changeOf returns the change with the id in st.
func changeOf(st state.State, id state.ChangeID) (state.Change, bool) {
	for _, c := range st.Changes {
		if c.ID == id {
			return c, true
		}
	}
	return state.Change{}, false
}

// drive follows the change to its end: every question it asks is answered
// with yes, and the kinds of the questions are returned with the final
// change.
func (r *rig) drive(t *testing.T, id state.ChangeID) (state.Change, []change.QuestionKind) {
	t.Helper()
	var asked []change.QuestionKind
	var answered change.QuestionID
	version := uint64(0)
	for {
		st := r.wait(t, version, func(st state.State) bool {
			c, ok := changeOf(st, id)
			return ok && (c.State == change.Finished || (c.Question != nil && c.Question.ID != answered))
		})
		version = st.Version
		c, _ := changeOf(st, id)
		if c.State == change.Finished {
			return c, asked
		}
		asked = append(asked, c.Question.Kind)
		answered = c.Question.ID
		if err := r.b.Answer(t.Context(), id, c.Question.ID, true); err != nil {
			t.Fatalf("Answer: %v", err)
		}
	}
}

// propose dry-runs a and returns the proposal.
func (r *rig) propose(t *testing.T, a state.Action) state.Proposal {
	t.Helper()
	p, err := r.b.Plan(t.Context(), a)
	if err != nil {
		t.Fatalf("Plan %s: %v", a.Kind, err)
	}
	return p
}

func TestAddonUpdateRunsToTheEnd(t *testing.T) {
	world := changeWorld(func(s *fleettest.State, _ *fleettest.Cluster) {
		s.DefaultAddonUpdate = &fleettest.UpdateScript{After: 2 * time.Minute}
	})
	r := changeRig(t, world, false)
	first := r.wait(t, 0, swept(1))
	if first.ReadOnly {
		t.Fatal("a backend with --allow-changes reads as read-only")
	}
	p := r.propose(t, state.Action{Kind: state.ActionAddons, Cluster: prod})
	if p.Blocked || p.Doc.Operation != apidoc.OperationAddonUpdate || len(p.Doc.Clusters) != 1 || p.Doc.Clusters[0].Steps[0].Action != apidoc.ActionSubmit {
		t.Fatalf("proposal = %+v", p.Doc)
	}
	if want := "refresh --profile test --region us-east-1 addon update --all -c prod"; p.Command != want {
		t.Fatalf("command = %q, want %q", p.Command, want)
	}
	id, err := r.b.Start(t.Context(), p.ID)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	// The proposal is consumed: the same confirmation cannot start again.
	if _, err := r.b.Start(t.Context(), p.ID); !errors.Is(err, backend.ErrStaleProposal) {
		t.Fatalf("second Start = %v, want ErrStaleProposal", err)
	}
	c, asked := r.drive(t, id)
	if len(asked) != 0 {
		t.Fatalf("an unchanged plan asked %v", asked)
	}
	if c.Elsewhere || c.Kind != change.AddonUpdate || c.Target.Cluster != "prod" || c.Plan == nil {
		t.Fatalf("change = %+v", c.View)
	}
	if c.Result == nil || c.Result.Status != change.Succeeded {
		t.Fatalf("result = %+v", c.Result)
	}
	if len(c.Steps) != 2 || c.Steps[0].Status != change.StepSucceeded || c.Steps[0].Ref.Name != "vpc-cni" || c.Steps[1].Status != change.StepSkipped {
		t.Fatalf("steps = %+v", c.Steps)
	}
	if len(c.Log) == 0 || c.Log[0].Kind != change.PlanReady {
		t.Fatalf("log = %+v", c.Log)
	}
	// The claim is released, the feed says how it ended, and the cluster
	// is read again on its own, without a sweep.
	after := r.wait(t, 0, func(st state.State) bool {
		return st.Clusters[0].Change == 0 && st.Clusters[0].Stale.Addons == 0 && strings.Contains(feedText(st), "prod ended: succeeded")
	})
	if after.Sweep.Count != 1 {
		t.Fatalf("the end of a change swept the fleet (%d sweeps)", after.Sweep.Count)
	}
	if got := after.Clusters[0].Snapshot.Addons[1].Version; got != cniNew {
		t.Fatalf("vpc-cni after the refetch = %s, want %s", got, cniNew)
	}
}

// One confirmation starts one change, however many times it arrives.
func TestOneConfirmationStartsOneChange(t *testing.T) {
	r := changeRig(t, changeWorld(nil), false)
	r.wait(t, 0, swept(1))
	p := r.propose(t, state.Action{Kind: state.ActionAddons, Cluster: prod})
	var wg sync.WaitGroup
	results := make([]error, 32)
	for i := range results {
		wg.Go(func() { _, results[i] = r.b.Start(context.Background(), p.ID) })
	}
	wg.Wait()
	started := 0
	for _, err := range results {
		switch {
		case err == nil:
			started++
		case !errors.Is(err, backend.ErrStaleProposal):
			t.Errorf("Start = %v", err)
		}
	}
	if started != 1 {
		t.Fatalf("%d changes started from one confirmation", started)
	}
	st := r.wait(t, 0, func(st state.State) bool { return len(st.Changes) == 1 && st.Changes[0].State == change.Finished })
	updates := 0
	for _, req := range r.server.Requests() {
		if req.Operation == "eks:UpdateAddon" {
			updates++
		}
	}
	if updates != 1 {
		t.Fatalf("%d UpdateAddon calls for one change", updates)
	}
	if st.Changes[0].Result.Status != change.Succeeded {
		t.Fatalf("result = %s", st.Changes[0].Result.Status)
	}
}

// A newer dry run replaces the one before: the older confirmation is
// stale, whichever cluster it was for.
func TestOnlyTheNewestProposalCanStart(t *testing.T) {
	r := changeRig(t, changeWorld(nil), false)
	r.wait(t, 0, swept(1))
	first := r.propose(t, state.Action{Kind: state.ActionAddons, Cluster: prod})
	second := r.propose(t, state.Action{Kind: state.ActionRoll, Cluster: prod, Nodegroup: "web"})
	if _, err := r.b.Start(t.Context(), first.ID); !errors.Is(err, backend.ErrStaleProposal) {
		t.Fatalf("Start of the older proposal = %v, want ErrStaleProposal", err)
	}
	if second.Blocked || second.Doc.Operation != apidoc.OperationNodegroupUpdate {
		t.Fatalf("roll proposal = %+v", second.Doc)
	}
	if _, err := r.b.Plan(t.Context(), state.Action{Kind: state.ActionAddons, Cluster: state.Key{Region: east, Name: "nope"}}); !errors.Is(err, backend.ErrUnknownCluster) {
		t.Fatalf("Plan of an unknown cluster = %v", err)
	}
}

// A blocked dry run cannot start: a rollback to a version that EKS no
// longer offers is blocked.
func TestBlockedProposalCannotStart(t *testing.T) {
	world := changeWorld(func(s *fleettest.State, c *fleettest.Cluster) {
		s.ClusterVersions = slices.DeleteFunc(s.ClusterVersions, func(v fleettest.ClusterVersion) bool { return v.Version == "1.32" })
		c.Updates = append(c.Updates, upgradeTo133())
	})
	r := changeRig(t, world, false)
	r.wait(t, 0, swept(1))
	p := r.propose(t, state.Action{Kind: state.ActionRollback, Cluster: prod})
	if !p.Blocked || len(p.Doc.Clusters[0].Blockers) == 0 {
		t.Fatalf("rollback to an unoffered version is not blocked: %+v", p.Doc.Clusters[0])
	}
	if _, err := r.b.Start(t.Context(), p.ID); err == nil || !strings.Contains(err.Error(), "blocked") {
		t.Fatalf("Start of a blocked proposal = %v", err)
	}
	for _, req := range r.server.Requests() {
		if strings.HasPrefix(string(req.Operation), "eks:Update") {
			t.Fatalf("a blocked proposal sent %s", req.Operation)
		}
	}
}

// upgradeTo133 is the update record of an upgrade to 1.33 two hours ago:
// inside the rollback window.
func upgradeTo133() *fleettest.Update {
	return &fleettest.Update{ID: "up-1", Type: fleettest.TypeVersionUpdate, Status: fleettest.UpdateSuccessful,
		Params: []fleettest.UpdateParam{{Type: "Version", Value: "1.33"}}, CreatedAt: cmdutiltest.Now.Add(-2 * time.Hour)}
}

// A plan that changed between the dry run and the start asks before
// anything runs; while it asks, the cluster is claimed: a second change
// on it is refused, and a sweep adopts nothing on it. n to the question
// declines, and nothing was sent.
func TestPlanChangedAsksAndOneChangePerCluster(t *testing.T) {
	r := changeRig(t, changeWorld(nil), false)
	r.wait(t, 0, swept(1))
	p := r.propose(t, state.Action{Kind: state.ActionAddons, Cluster: prod})
	// A newer vpc-cni appears after the dry run: the plan's target moves.
	r.world.Edit(func(s *fleettest.State) {
		s.AddonVersions = append(s.AddonVersions, fleettest.AddonVersion{Addon: "vpc-cni", Version: "v1.21.0-eksbuild.1", ClusterVersions: []string{"1.33"}})
	})
	id, err := r.b.Start(t.Context(), p.ID)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	asking := r.wait(t, 0, func(st state.State) bool {
		c, ok := changeOf(st, id)
		return ok && c.State == change.Asking
	})
	c, _ := changeOf(asking, id)
	if c.Question.Kind != change.PlanChanged || !c.Controls.Answer {
		t.Fatalf("question = %+v, controls %+v", c.Question, c.Controls)
	}
	// One change per cluster.
	again := r.propose(t, state.Action{Kind: state.ActionRoll, Cluster: prod, Nodegroup: "web"})
	if _, err := r.b.Start(t.Context(), again.ID); !errors.Is(err, backend.ErrClusterBusy) {
		t.Fatalf("Start on a claimed cluster = %v, want ErrClusterBusy", err)
	}
	// A sweep while it asks adopts nothing on the claimed cluster.
	r.b.Refresh()
	r.after <- cmdutiltest.Now
	swept2 := r.wait(t, asking.Version, swept(2))
	if len(swept2.Changes) != 1 {
		t.Fatalf("%d changes after a sweep, want the one that asks", len(swept2.Changes))
	}
	// A stale question id is refused; the real one declines.
	if err := r.b.Answer(t.Context(), id, c.Question.ID+1, true); !errors.Is(err, change.ErrStaleQuestion) {
		t.Fatalf("Answer with a stale id = %v", err)
	}
	if err := r.b.Answer(t.Context(), id, c.Question.ID, false); err != nil {
		t.Fatalf("Answer: %v", err)
	}
	done := r.wait(t, 0, func(st state.State) bool {
		c, ok := changeOf(st, id)
		return ok && c.State == change.Finished
	})
	c, _ = changeOf(done, id)
	if c.Result.Status != change.Declined {
		t.Fatalf("result = %s, want Declined", c.Result.Status)
	}
	for _, req := range r.server.Requests() {
		if req.Operation == "eks:UpdateAddon" {
			t.Fatal("a declined change sent UpdateAddon")
		}
	}
	if !strings.Contains(feedText(done), "prod ended: declined") {
		t.Fatalf("feed:\n%s", feedText(done))
	}
}

// A roll is followed through the node view: the old nodes drain and the
// new nodes join, and the node events reach the state.
func TestRollShowsTheNodes(t *testing.T) {
	world := changeWorld(func(s *fleettest.State, _ *fleettest.Cluster) {
		s.DefaultNodegroupUpdate = &fleettest.UpdateScript{After: 4 * time.Minute}
	})
	r := pacedRig(t, world, true, newPacer(t, world))
	first := r.wait(t, 0, swept(1))
	if first.Clusters[0].Stale.AMIs != 1 {
		t.Fatalf("web is not stale: %+v", first.Clusters[0].Stale)
	}
	p := r.propose(t, state.Action{Kind: state.ActionRoll, Cluster: prod, Nodegroup: "web"})
	if p.Blocked || p.Doc.Clusters[0].Gate == nil {
		t.Fatalf("proposal = %+v", p.Doc.Clusters[0])
	}
	if !strings.Contains(p.Command, "nodegroup update web -c prod") {
		t.Fatalf("command = %q", p.Command)
	}
	id, err := r.b.Start(t.Context(), p.ID)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	c, _ := r.drive(t, id)
	if c.Result.Status != change.Succeeded {
		t.Fatalf("result = %s (%v); log %+v", c.Result.Status, c.Result.Err, c.Log)
	}
	step := c.Steps[0]
	nodes := c.Progress[step.Ref].Nodes
	if nodes == nil || nodes.Snapshot.ReadyTarget != 2 {
		t.Fatalf("node view = %+v; log %s", nodes, logText(c))
	}
	// The new nodes join at the start of the roll, in the baseline of the
	// observer; what follows is seen stage by stage.
	kinds := map[noderoll.EventKind]bool{}
	for _, e := range c.Nodes[step.Ref] {
		kinds[e.Kind] = true
	}
	for _, k := range []noderoll.EventKind{noderoll.EvtOnline, noderoll.EvtDraining, noderoll.EvtTerminated} {
		if !kinds[k] {
			t.Errorf("no %s node event: %v", k, c.Nodes[step.Ref])
		}
	}
}

// A roll whose drain is stuck fails with the update, and says so in the
// nodes: the old nodes stay draining with their pods.
func TestStuckDrainFailsTheRoll(t *testing.T) {
	world := changeWorld(func(s *fleettest.State, _ *fleettest.Cluster) {
		s.DefaultNodegroupUpdate = &fleettest.UpdateScript{After: 4 * time.Minute, Result: fleettest.UpdateFailed, Roll: fleettest.RollScript{StuckDrain: true},
			Errors: []fleettest.UpdateError{{Code: "PodEvictionFailure", Message: "the PDB web lets no pod go"}}}
	})
	r := pacedRig(t, world, true, newPacer(t, world))
	r.wait(t, 0, swept(1))
	p := r.propose(t, state.Action{Kind: state.ActionRoll, Cluster: prod, Nodegroup: "web"})
	id, err := r.b.Start(t.Context(), p.ID)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	c, _ := r.drive(t, id)
	if c.Result.Status != change.Failed {
		t.Fatalf("result = %s, want Failed", c.Result.Status)
	}
	if nodes := c.Progress[c.Steps[0].Ref].Nodes; nodes == nil || nodes.Snapshot.Draining == 0 {
		t.Fatalf("node view = %+v", nodes)
	}
	if !strings.Contains(feedText(r.wait(t, 0, func(st state.State) bool { return strings.Contains(feedText(st), "ended: failed") })), "PodEvictionFailure") {
		t.Fatal("the feed does not name the failure")
	}
}

// An upgrade pauses before its next phase, resumes, and stops before the
// phase after: the phases that did not start are Stopped, and a stop
// after the last checkpoint is too late.
func TestUpgradePausesAndStops(t *testing.T) {
	world := changeWorld(func(s *fleettest.State, _ *fleettest.Cluster) {
		s.DefaultUpdate = fleettest.UpdateScript{After: time.Minute}
	})
	r := changeRig(t, world, true)
	r.wait(t, 0, swept(1))
	p := r.propose(t, state.Action{Kind: state.ActionUpgrade, Cluster: prod, To: fleet.Version{Major: 1, Minor: 34}})
	if p.Blocked || p.Doc.Operation != apidoc.OperationClusterUpgrade || len(p.Doc.Clusters[0].Steps) < 3 {
		t.Fatalf("proposal = %+v", p.Doc.Clusters[0])
	}
	if !strings.Contains(p.Command, "cluster upgrade prod --to 1.34") {
		t.Fatalf("command = %q", p.Command)
	}
	id, err := r.b.Start(t.Context(), p.ID)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	// The pause is asked for before the run reaches its first checkpoint.
	if err := r.b.Pause(t.Context(), id); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	paused := r.wait(t, 0, func(st state.State) bool {
		c, ok := changeOf(st, id)
		return ok && c.State == change.Paused
	})
	c, _ := changeOf(paused, id)
	if !c.Controls.Pause || !c.Controls.Stop || c.Controls.Answer {
		t.Fatalf("controls while paused = %+v", c.Controls)
	}
	if err := r.b.Pause(t.Context(), id); err != nil {
		t.Fatalf("a second Pause while paused = %v", err)
	}
	if err := r.b.Resume(t.Context(), id); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	// The stop lands at the next checkpoint: the phase that runs ends, the
	// rest never start.
	if err := r.b.Stop(t.Context(), id); err != nil && !errors.Is(err, change.ErrTooLate) {
		t.Fatalf("Stop: %v", err)
	}
	c, _ = r.drive(t, id)
	if c.Result.Status != change.Stopped && c.Result.Status != change.Succeeded {
		t.Fatalf("result = %s (%v)", c.Result.Status, c.Result.Err)
	}
	if c.Result.Status == change.Stopped {
		stopped := 0
		for _, ph := range c.Phases {
			if ph.Status == change.PhaseStopped {
				stopped++
			}
		}
		if stopped == 0 {
			t.Fatalf("a stopped run has no stopped phase: %+v", c.Phases)
		}
		if !c.StopRequested {
			t.Fatal("the view does not say that a stop was requested")
		}
	}
	// Nothing steers a finished change.
	for name, err := range map[string]error{"Pause": r.b.Pause(t.Context(), id), "Resume": r.b.Resume(t.Context(), id), "Stop": r.b.Stop(t.Context(), id)} {
		if !errors.Is(err, change.ErrFinished) && !errors.Is(err, change.ErrTooLate) {
			t.Errorf("%s after the end = %v", name, err)
		}
	}
}

// A stop before the first checkpoint ends the run before anything is
// sent.
func TestStopBeforeTheFirstPhaseSendsNothing(t *testing.T) {
	world := changeWorld(func(s *fleettest.State, _ *fleettest.Cluster) {
		s.DefaultUpdate = fleettest.UpdateScript{After: time.Minute}
	})
	r := changeRig(t, world, true)
	r.wait(t, 0, swept(1))
	p := r.propose(t, state.Action{Kind: state.ActionUpgrade, Cluster: prod, To: fleet.Version{Major: 1, Minor: 34}, Only: []change.Part{change.PartControlPlane}})
	id, err := r.b.Start(t.Context(), p.ID)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := r.b.Pause(t.Context(), id); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	r.wait(t, 0, func(st state.State) bool {
		c, ok := changeOf(st, id)
		return ok && c.State == change.Paused
	})
	if err := r.b.Stop(t.Context(), id); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	c, _ := r.drive(t, id)
	if c.Result.Status != change.Stopped {
		t.Fatalf("result = %s", c.Result.Status)
	}
	for _, req := range r.server.Requests() {
		if req.Operation == "eks:UpdateClusterVersion" {
			t.Fatal("a run stopped before its first phase sent UpdateClusterVersion")
		}
	}
	// A step that the plan leaves out is Skipped; no step did any work.
	for _, s := range c.Steps {
		if s.Status != change.StepNotAttempted && s.Status != change.StepSkipped {
			t.Fatalf("step %s = %s", s.Ref.Name, s.Status)
		}
	}
}

// A rollback is offered from the window that the readiness run found, and
// runs the rollback planner.
func TestRollbackOnTheWindow(t *testing.T) {
	world := changeWorld(func(s *fleettest.State, c *fleettest.Cluster) {
		s.DefaultUpdate = fleettest.UpdateScript{After: time.Minute}
		c.Updates = append(c.Updates, upgradeTo133())
	})
	r := changeRig(t, world, true)
	r.wait(t, 0, swept(1))
	if err := r.b.Readiness(t.Context(), prod); err != nil {
		t.Fatal(err)
	}
	ready := r.wait(t, 0, func(st state.State) bool {
		return st.Clusters[0].Readiness != nil && !st.Clusters[0].Readiness.Running()
	})
	if ready.Clusters[0].Rollback == nil {
		t.Fatalf("no rollback window: %+v", ready.Clusters[0].Readiness)
	}
	p := r.propose(t, state.Action{Kind: state.ActionRollback, Cluster: prod})
	if p.Blocked || p.Doc.Operation != apidoc.OperationClusterRollback || !strings.Contains(p.Command, "cluster rollback prod") {
		t.Fatalf("proposal = %+v, command %q", p.Doc.Clusters[0], p.Command)
	}
	id, err := r.b.Start(t.Context(), p.ID)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	c, _ := r.drive(t, id)
	if c.Result.Status != change.Succeeded || c.Kind != change.ClusterRollback {
		t.Fatalf("result = %s (%v), kind %s", c.Result.Status, c.Result.Err, c.Kind)
	}
	after := r.wait(t, 0, func(st state.State) bool { return st.Clusters[0].Change == 0 && st.Clusters[0].Version.Minor == 32 })
	if after.Sweep.Count != 1 {
		t.Fatal("the refetch swept the fleet")
	}
}

// A roll that another process started is adopted by the next sweep: it
// shows as started elsewhere, with no control, and is watched to its end.
func TestAdoptsAChangeStartedElsewhere(t *testing.T) {
	world := changeWorld(func(s *fleettest.State, _ *fleettest.Cluster) {
		s.DefaultNodegroupUpdate = &fleettest.UpdateScript{After: 4 * time.Minute}
	})
	r := pacedRig(t, world, true, newPacer(t, world))
	first := r.wait(t, 0, swept(1))
	version := "1.33"
	u, err := world.UpdateNodegroupVersion(east, "prod", "web", fleettest.NodegroupVersionRequest{Version: &version})
	if err != nil {
		t.Fatal(err)
	}
	r.b.Refresh()
	adopted := r.wait(t, first.Version, func(st state.State) bool { return len(st.Changes) == 1 && st.Changes[0].Plan != nil })
	c := adopted.Changes[0]
	if !c.Elsewhere || c.Controls != (change.Controls{}) || c.Kind != change.NodegroupUpdate || adopted.Clusters[0].Change != c.ID {
		t.Fatalf("adopted change = elsewhere %v, controls %+v, kind %s, cluster change %d", c.Elsewhere, c.Controls, c.Kind, adopted.Clusters[0].Change)
	}
	if !strings.Contains(feedText(adopted), "watching a change started elsewhere: nodegroup web") {
		t.Fatalf("feed:\n%s", feedText(adopted))
	}
	for name, err := range map[string]error{"Pause": r.b.Pause(t.Context(), c.ID), "Resume": r.b.Resume(t.Context(), c.ID), "Stop": r.b.Stop(t.Context(), c.ID), "Answer": r.b.Answer(t.Context(), c.ID, 1, true)} {
		if !errors.Is(err, change.ErrNoControls) {
			t.Errorf("%s on a change started elsewhere = %v, want ErrNoControls", name, err)
		}
	}
	// A second sweep while it runs does not watch it twice.
	r.b.Refresh()
	r.after <- cmdutiltest.Now
	if st := r.wait(t, adopted.Version, swept(3)); len(st.Changes) != 1 {
		t.Fatalf("%d changes after a second sweep", len(st.Changes))
	}
	done, _ := r.drive(t, c.ID)
	if done.Result.Status != change.Succeeded || len(done.Steps) != 1 || !done.Steps[0].Attached || done.Steps[0].UpdateID != u.ID {
		t.Fatalf("watched change = %s, steps %+v", done.Result.Status, done.Steps)
	}
	if done.Progress[done.Steps[0].Ref].Nodes == nil {
		t.Fatal("the watched roll has no node view")
	}
	// Starting a change of our own on the cluster was refused meanwhile,
	// and is allowed once the watch ended.
	after := r.wait(t, 0, func(st state.State) bool { return st.Clusters[0].Change == 0 })
	if strings.Contains(feedText(after), "watching a change started elsewhere: nodegroup web\n"+"prod watching") {
		t.Fatal("the roll was watched twice")
	}
}

// logText is the texts of the notices and the steps of a change.
func logText(c state.Change) string {
	var out []string
	for _, e := range c.Log {
		out = append(out, string(e.Kind)+" "+e.Text)
	}
	return strings.Join(out, "\n")
}
