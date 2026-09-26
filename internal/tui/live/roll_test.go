package live

import (
	"context"
	"errors"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/dantech2000/refresh/internal/diag"
	"github.com/dantech2000/refresh/internal/health"
	"github.com/dantech2000/refresh/internal/noderoll"
	nodegroupsvc "github.com/dantech2000/refresh/internal/services/nodegroup"
	statussvc "github.com/dantech2000/refresh/internal/services/status"
	"github.com/dantech2000/refresh/internal/tui/state"
	"github.com/dantech2000/refresh/internal/types"
)

// scriptedObs replays noderoll.DemoTimeline and signals when it has shown
// the last frame.
type scriptedObs struct {
	*noderoll.ScriptedObserver
	atEnd chan struct{}
}

func (s scriptedObs) StartInformers(context.Context) error {
	return errors.New("no informers in tests")
}
func (s scriptedObs) StopInformers()                        {}
func (s scriptedObs) CaptureBaseline(context.Context) error { return nil }

func (s scriptedObs) Snapshot(ctx context.Context) (noderoll.Snapshot, error) {
	snap, err := s.ScriptedObserver.Snapshot(ctx)
	if s.AtEnd() {
		select {
		case s.atEnd <- struct{}{}:
		default:
		}
	}
	return snap, err
}

// rollRig is a backend with changes on and fake roll services.
type rollRig struct {
	b       *Backend
	started atomic.Int64
	version atomic.Value // the version the roll was pinned to
	health  health.Decision
	// warn adds a Warn-level health result.
	warn string
	live ekstypes.Nodegroup // what the live describe returns
	// decisions override the decision table's answer, by nodegroup.
	decisions map[string]nodegroupsvc.AMIUpdateDecision
	// offerings, verification: what those services return.
	offerings    []nodegroupsvc.UnavailableOffering
	verification nodegroupsvc.PostRollVerification
	verified     atomic.Int64
	release      chan ekstypes.UpdateStatus
	atEnd        chan struct{}
	// drainBlocked blocks the drain gate; drainErr fails its PDB read.
	drainBlocked bool
	drainErr     error
}

// start dry-runs a and starts it, as the TUI does (p, then y).
func (rig *rollRig) start(t *testing.T, a state.Action) error {
	t.Helper()
	if _, err := rig.b.Plan(t.Context(), a); err != nil {
		return err
	}
	return rig.b.Start(t.Context(), a)
}

func newRollRig(t *testing.T) *rollRig {
	t.Helper()
	rows := prodRows()
	rows[0].Nodegroups[1].Status = "ACTIVE" // nothing in flight
	f := &fleet{rows: map[string][]statussvc.ClusterStatus{"us-east-1": rows}}
	rig := &rollRig{b: newTestBackend(t, f, "us-east-1"), health: health.DecisionProceed,
		release: make(chan ekstypes.UpdateStatus, 1), atEnd: make(chan struct{}, 1),
		decisions: map[string]nodegroupsvc.AMIUpdateDecision{
			"ng-system": {Action: types.ActionSkipLatest, Reason: "already on latest AMI"},
		},
		live: ekstypes.Nodegroup{Version: aws.String("1.31"), Status: ekstypes.NodegroupStatusActive,
			ScalingConfig: &ekstypes.NodegroupScalingConfig{DesiredSize: aws.Int32(6)},
			UpdateConfig:  &ekstypes.NodegroupUpdateConfig{MaxUnavailable: aws.Int32(2)}}}
	b := rig.b
	b.opts.AllowChanges = true
	b.opts.ObserveInterval = time.Millisecond
	b.roll = rollServices{
		kubeFor: func(context.Context, aws.Config, string) (kubernetes.Interface, health.NodeMetricsLister, string) {
			return fake.NewClientset(), nil, "kubeconfig context prod-api"
		},
		decide: func(_ context.Context, _ aws.Config, _, ng string) (*ekstypes.Nodegroup, nodegroupsvc.AMIUpdateDecision, error) {
			live := rig.live
			d := nodegroupsvc.AMIUpdateDecision{Action: types.ActionUpdate, Reason: "AMI is outdated"}
			if dd, ok := rig.decisions[ng]; ok {
				d = dd
			}
			return &live, d, nil
		},
		healthCheck: func(context.Context, aws.Config, string, []string, kubernetes.Interface, health.NodeMetricsLister) health.HealthSummary {
			s := health.HealthSummary{Decision: rig.health, Results: []health.HealthResult{{Name: "nodes Ready", Status: health.StatusPass}}}
			if rig.warn != "" {
				s.Results = append(s.Results, health.HealthResult{Name: rig.warn, Status: health.StatusWarn})
			}
			if rig.health == health.DecisionBlock {
				s.Results = append(s.Results, health.HealthResult{Name: "PDB drain blockers", Status: health.StatusFail, IsBlocking: true, Message: "checkout allows 0 disruptions"})
			}
			return s
		},
		drainBlockers: func(context.Context, aws.Config, string, string, kubernetes.Interface) (health.DrainBlockerReport, error) {
			if rig.drainErr != nil {
				return health.DrainBlockerReport{}, rig.drainErr
			}
			r := health.DrainBlockerReport{Scoped: true}
			if rig.drainBlocked {
				r.Blockers = []health.PDBInfo{{Namespace: "default", Name: "web"}}
			}
			return r, nil
		},
		startRoll: func(_ context.Context, _ aws.Config, cluster, ng, version string) (*ekstypes.Update, error) {
			rig.started.Add(1)
			rig.version.Store(cluster + "/" + ng + "@" + version)
			return &ekstypes.Update{Id: aws.String("upd-1")}, nil
		},
		waitUpdate: func(ctx context.Context, _ aws.Config, _, _, _ string, _ time.Duration) (ekstypes.UpdateStatus, string, error) {
			select {
			case s := <-rig.release:
				// As monitoring.MonitorUpdates: an error for a failed,
				// cancelled, or unfinished update.
				switch s {
				case ekstypes.UpdateStatusSuccessful:
					return s, "", nil
				case ekstypes.UpdateStatusInProgress:
					return s, "", errors.New("monitoring timeout reached")
				default:
					return s, "PodEvictionFailure", errors.New("one or more nodegroup updates failed")
				}
			case <-ctx.Done():
				return ekstypes.UpdateStatusInProgress, "", ctx.Err()
			}
		},
		offerings: func(context.Context, aws.Config, string, string) ([]nodegroupsvc.UnavailableOffering, error) {
			return rig.offerings, nil
		},
		pendingPods: func(context.Context, kubernetes.Interface) (nodegroupsvc.PendingPods, bool) {
			return nodegroupsvc.PendingPods{}, true
		},
		verify: func(context.Context, aws.Config, string, string, kubernetes.Interface, nodegroupsvc.PendingPods, bool) (nodegroupsvc.PostRollVerification, []diag.Failure) {
			rig.verified.Add(1)
			return rig.verification, nil
		},
		observe: func(kubernetes.Interface, string) rollObserver {
			return scriptedObs{ScriptedObserver: noderoll.NewScriptedObserver(noderoll.DemoTimeline()), atEnd: rig.atEnd}
		},
	}
	b.sweep(t.Context())
	return rig
}

var roll = state.Action{Kind: state.ActionRoll, Cluster: "prod-api", Nodegroup: "ng-general"}

func TestRollStartsIsWatchedAndFinishes(t *testing.T) {
	rig := newRollRig(t)
	b := rig.b
	plan, err := b.Plan(t.Context(), roll)
	if err != nil || plan.Blocked != "" {
		t.Fatalf("plan = %+v, %v", plan, err)
	}
	if plan.Gates[0].Text != "live node view" || plan.Gates[0].Note != "kubeconfig context prod-api" {
		t.Fatalf("gates = %+v", plan.Gates)
	}
	if err := b.Start(t.Context(), roll); err != nil {
		t.Fatal(err)
	}
	if got := rig.version.Load(); got != "prod-api/ng-general@1.31" {
		t.Fatalf("roll pinned to %v, want the nodegroup's own version", got)
	}
	st, _ := b.State(t.Context())
	if len(st.Rolls) != 1 || !st.Rolls[0].Running() || st.Badge != "CHANGES ON" {
		t.Fatalf("rolls = %+v, badge %q", st.Rolls, st.Badge)
	}
	for _, c := range st.Clusters {
		if c.Name == "prod-api" && c.Busy != "rolling ng-general" {
			t.Fatalf("Busy = %q", c.Busy)
		}
	}
	// One change per cluster.
	if err := b.Start(t.Context(), roll); err == nil || !strings.Contains(err.Error(), "busy") {
		t.Fatalf("second Start = %v, want busy", err)
	}
	if p, _ := b.Plan(t.Context(), roll); !strings.Contains(p.Blocked, "busy: rolling ng-general") {
		t.Fatalf("plan during the roll blocked = %q", p.Blocked)
	}

	<-rig.atEnd // the node view has seen the whole roll
	rig.release <- ekstypes.UpdateStatusSuccessful
	b.Close()

	st, _ = b.State(t.Context())
	r := st.Rolls[0]
	if r.Running() || r.Failed != "" || r.Replaced() != 3 || r.MaxUnavailableText != "2" {
		t.Fatalf("finished roll = %+v", r)
	}
	text := joinText(r.Events)
	for _, want := range []string{"UpdateNodegroupVersion id=upd-1", "online", "draining", "terminated", "roll complete"} {
		if !strings.Contains(text, want) {
			t.Errorf("roll feed lacks %q:\n%s", want, text)
		}
	}
	for _, c := range st.Clusters {
		if c.Name == "prod-api" && c.Busy != "" {
			t.Fatalf("still busy after the roll: %q", c.Busy)
		}
	}
	select {
	case <-b.wake:
	default:
		t.Fatal("a finished roll did not ask for a sweep")
	}
}

func TestHealthGateBlocksTheRoll(t *testing.T) {
	rig := newRollRig(t)
	rig.health = health.DecisionBlock
	p, _ := rig.b.Plan(t.Context(), roll)
	if !strings.Contains(p.Blocked, "health gate blocks the roll: PDB drain blockers") {
		t.Fatalf("plan blocked = %q", p.Blocked)
	}
	err := rig.start(t, roll)
	if err == nil || rig.started.Load() != 0 {
		t.Fatalf("Start = %v with %d rolls started; the gate must stop it before any change", err, rig.started.Load())
	}
	// The claim is released: once the gate passes, the roll can start.
	rig.health = health.DecisionProceed
	if err := rig.start(t, roll); err != nil {
		t.Fatalf("Start after the gate passed = %v", err)
	}
	rig.release <- ekstypes.UpdateStatusSuccessful
	rig.b.Close()
}

func TestFailedAndUnwatchedRolls(t *testing.T) {
	for name, tc := range map[string]struct {
		status ekstypes.UpdateStatus
		want   string
	}{
		"failed":    {ekstypes.UpdateStatusFailed, "Failed PodEvictionFailure"},
		"cancelled": {ekstypes.UpdateStatusCancelled, "Cancelled"},
		"timeout":   {ekstypes.UpdateStatusInProgress, "outcome unknown"},
	} {
		t.Run(name, func(t *testing.T) {
			rig := newRollRig(t)
			if err := rig.start(t, roll); err != nil {
				t.Fatal(err)
			}
			rig.release <- tc.status
			rig.b.Close()
			st, _ := rig.b.State(t.Context())
			if r := st.Rolls[0]; r.Running() || !strings.Contains(r.Failed, tc.want) {
				t.Fatalf("roll = %+v", r)
			}
		})
	}
}

func TestStartErrorReleasesTheCluster(t *testing.T) {
	rig := newRollRig(t)
	rig.b.roll.startRoll = func(context.Context, aws.Config, string, string, string) (*ekstypes.Update, error) {
		return nil, errors.New("InvalidRequestException: nodegroup is updating")
	}
	if err := rig.start(t, roll); err == nil || !strings.Contains(err.Error(), "InvalidRequestException") {
		t.Fatalf("Start = %v", err)
	}
	st, _ := rig.b.State(t.Context())
	for _, c := range st.Clusters {
		if c.Name == "prod-api" && c.Busy != "" {
			t.Fatalf("a failed start left the cluster busy: %q", c.Busy)
		}
	}
	if len(st.Rolls) != 0 {
		t.Fatal("a failed start left a roll")
	}
}

func TestACurrentNodegroupDoesNotRoll(t *testing.T) {
	rig := newRollRig(t)
	current := state.Action{Kind: state.ActionRoll, Cluster: "prod-api", Nodegroup: "ng-system"}
	rig.b.mu.Lock()
	rig.b.accepted[acceptKey(rig.b.targets["prod-api"], "ng-system")] = acceptedRoll{} // as a dry run would
	rig.b.mu.Unlock()
	if err := rig.b.Start(t.Context(), current); err == nil || !strings.Contains(err.Error(), "already on latest AMI") || rig.started.Load() != 0 {
		t.Fatalf("a roll of a current nodegroup started: %v", err)
	}
}

func TestNoKubeAccessStillRollsWithoutTheNodeView(t *testing.T) {
	rig := newRollRig(t)
	rig.b.roll.kubeFor = func(context.Context, aws.Config, string) (kubernetes.Interface, health.NodeMetricsLister, string) {
		return nil, nil, "no kubeconfig context for this cluster · add one: aws eks update-kubeconfig --name prod-api --region us-east-1"
	}
	p, _ := rig.b.Plan(t.Context(), roll)
	if p.Gates[0].Status != state.CheckWarn || p.Blocked != "" {
		t.Fatalf("plan = %+v", p)
	}
	if err := rig.b.Start(t.Context(), roll); err != nil {
		t.Fatal(err)
	}
	rig.release <- ekstypes.UpdateStatusSuccessful
	rig.b.Close()
	st, _ := rig.b.State(t.Context())
	if r := st.Rolls[0]; r.Running() || r.Failed != "" || !strings.Contains(joinText(r.Events), "update-kubeconfig") {
		t.Fatalf("roll = %+v", r)
	}
}

func TestStartNeedsTheDryRun(t *testing.T) {
	rig := newRollRig(t)
	if err := rig.b.Start(t.Context(), roll); err == nil || !strings.Contains(err.Error(), "no dry run") || rig.started.Load() != 0 {
		t.Fatalf("Start without a dry run = %v", err)
	}
}

func TestNewHealthFindingsSinceTheDryRunStopTheRoll(t *testing.T) {
	rig := newRollRig(t)
	rig.warn = "node utilization"
	if _, err := rig.b.Plan(t.Context(), roll); err != nil {
		t.Fatal(err)
	}
	// The warning shown in the dry run was accepted with y: the roll starts.
	// A new one since then stops it.
	rig.warn = "PodDisruptionBudgets"
	err := rig.b.Start(t.Context(), roll)
	if err == nil || !strings.Contains(err.Error(), "found more since the dry run: PodDisruptionBudgets (Warn)") || rig.started.Load() != 0 {
		t.Fatalf("Start = %v", err)
	}
	// With the new finding shown in a fresh dry run, it starts.
	if err := rig.start(t, roll); err != nil {
		t.Fatal(err)
	}
	rig.release <- ekstypes.UpdateStatusSuccessful
	rig.b.Close()
}

func TestTheDecisionTableDecidesAtStart(t *testing.T) {
	for name, d := range map[string]nodegroupsvc.AMIUpdateDecision{
		"updating": {Action: types.ActionSkipUpdating, Reason: "already updating"},
		"custom":   {Action: types.ActionSkipCustom, Reason: "custom AMI (AmiType=CUSTOM)"},
		"latest":   {Action: types.ActionSkipLatest, Reason: "already on latest AMI"},
	} {
		t.Run(name, func(t *testing.T) {
			rig := newRollRig(t)
			rig.decisions["ng-general"] = d
			if err := rig.start(t, roll); err == nil || !strings.Contains(err.Error(), d.Reason) || rig.started.Load() != 0 {
				t.Fatalf("Start = %v, want %q and no roll", err, d.Reason)
			}
		})
	}
	// A nodegroup that moved to another version since the dry run needs a
	// new dry run: the roll would replace nodes for a version the user
	// never saw.
	rig := newRollRig(t)
	rig.live.Version = aws.String("1.32")
	if err := rig.start(t, roll); err == nil || !strings.Contains(err.Error(), "the dry run showed 1.31") || rig.started.Load() != 0 {
		t.Fatalf("Start = %v, want a refusal and no roll", err)
	}
	rig.b.Close()
	// The version pinned is the one EKS reports, which the dry run showed.
	rig = newRollRig(t)
	if err := rig.start(t, roll); err != nil {
		t.Fatal(err)
	}
	if got := rig.version.Load(); got != "prod-api/ng-general@1.31" {
		t.Fatalf("pinned %v, want the live version", got)
	}
	rig.release <- ekstypes.UpdateStatusSuccessful
	rig.b.Close()
}

// The roll runs the PDB drain gate of `nodegroup update` in its dry run and
// again at Start; the TUI has no --force, so a blocker refuses the roll.
func TestADrainBlockerStopsTheRoll(t *testing.T) {
	rig := newRollRig(t)
	rig.drainBlocked = true
	p, err := rig.b.Plan(t.Context(), roll)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(p.Blocked, "PDB default/web allows 0 disruptions") {
		t.Fatalf("dry run Blocked = %q, want the drain blocker", p.Blocked)
	}
	if err := rig.b.Start(t.Context(), roll); err == nil || !strings.Contains(err.Error(), "would stop draining") || rig.started.Load() != 0 {
		t.Fatalf("Start = %v, want a refusal and no roll", err)
	}
	// A blocker that appears after a clean dry run stops Start too.
	rig.drainBlocked = false
	if _, err := rig.b.Plan(t.Context(), roll); err != nil {
		t.Fatal(err)
	}
	rig.drainBlocked = true
	if err := rig.b.Start(t.Context(), roll); err == nil || !strings.Contains(err.Error(), "would stop draining") || rig.started.Load() != 0 {
		t.Fatalf("Start = %v, want a refusal and no roll", err)
	}
	// So does a PDB read that fails.
	rig.drainBlocked, rig.drainErr = false, errors.New("pdbs forbidden")
	if err := rig.b.Start(t.Context(), roll); err == nil || !strings.Contains(err.Error(), "could not check the PodDisruptionBudgets") || rig.started.Load() != 0 {
		t.Fatalf("Start = %v, want a refusal and no roll", err)
	}
	rig.b.Close()
}

func TestLosingTheNodeViewSinceTheDryRunStopsTheRoll(t *testing.T) {
	rig := newRollRig(t)
	rig.b.roll.healthCheck = func(_ context.Context, _ aws.Config, _ string, _ []string, kube kubernetes.Interface, _ health.NodeMetricsLister) health.HealthSummary {
		r := health.HealthResult{Name: "Pod Disruption Budgets", Status: health.StatusPass}
		if kube == nil {
			r.Skipped = true
		}
		return health.HealthSummary{Decision: health.DecisionProceed, Results: []health.HealthResult{r}}
	}
	if _, err := rig.b.Plan(t.Context(), roll); err != nil {
		t.Fatal(err)
	}
	// The token expires between p and y: the PDB check can no longer run.
	rig.b.roll.kubeFor = func(context.Context, aws.Config, string) (kubernetes.Interface, health.NodeMetricsLister, string) {
		return nil, nil, "no node view: token expired"
	}
	err := rig.b.Start(t.Context(), roll)
	if err == nil || !strings.Contains(err.Error(), "Pod Disruption Budgets (Skipped)") || rig.started.Load() != 0 {
		t.Fatalf("Start = %v; a check that passed in the dry run and cannot run now must stop the roll", err)
	}
}

func TestUnknownAMIStatusCanRoll(t *testing.T) {
	rig := newRollRig(t)
	rows := prodRows()
	rows[0].Nodegroups[0].AMIStatus = types.AMIUnknown
	rows[0].Nodegroups[1].Status = "ACTIVE"
	rig.b.svc.listStatuses = (&fleet{rows: map[string][]statussvc.ClusterStatus{"us-east-1": rows}}).list
	rig.b.sweep(t.Context())
	p, _ := rig.b.Plan(t.Context(), roll)
	if p.Blocked != "" {
		t.Fatalf("an unknown AMI status blocks: %q", p.Blocked)
	}
	if err := rig.b.Start(t.Context(), roll); err != nil {
		t.Fatal(err)
	}
	rig.release <- ekstypes.UpdateStatusSuccessful
	rig.b.Close()
}

func TestClaimSurvivesAKeyChange(t *testing.T) {
	rig := newRollRig(t)
	if err := rig.start(t, roll); err != nil {
		t.Fatal(err)
	}
	// A cluster named prod-api appears in eu-west-1: the rolling one becomes
	// prod-api@us-east-1. It is still busy and cannot take a second roll.
	rows := prodRows()
	rows[0].Nodegroups[1].Status = "ACTIVE"
	f := &fleet{rows: map[string][]statussvc.ClusterStatus{
		"us-east-1": rows,
		"eu-west-1": {{Name: "prod-api", Region: "eu-west-1", Version: "1.33"}},
	}}
	rig.b.opts.Regions = []string{"us-east-1", "eu-west-1"}
	rig.b.svc.listStatuses = f.list
	rig.b.sweep(t.Context())
	st, _ := rig.b.State(t.Context())
	busy := ""
	for _, c := range st.Clusters {
		if c.Name == "prod-api@us-east-1" {
			busy = c.Busy
		}
	}
	if busy != "rolling ng-general" || st.Rolls[0].Cluster != "prod-api@us-east-1" {
		t.Fatalf("busy %q, roll on %q", busy, st.Rolls[0].Cluster)
	}
	second := state.Action{Kind: state.ActionRoll, Cluster: "prod-api@us-east-1", Nodegroup: "ng-general"}
	if err := rig.start(t, second); err == nil || !strings.Contains(err.Error(), "busy") {
		t.Fatalf("second roll = %v", err)
	}
	rig.release <- ekstypes.UpdateStatusSuccessful
	rig.b.Close()
}

func TestVerificationRunsAfterASuccessfulRoll(t *testing.T) {
	rig := newRollRig(t)
	rig.verification = nodegroupsvc.PostRollVerification{Checks: []string{"nodegroup ng-general is ACTIVE", "no new Pending pods"}}
	if err := rig.start(t, roll); err != nil {
		t.Fatal(err)
	}
	rig.release <- ekstypes.UpdateStatusSuccessful
	rig.b.Close()
	st, _ := rig.b.State(t.Context())
	r := st.Rolls[0]
	if rig.verified.Load() != 1 || r.Failed != "" || len(r.Gates) != 2 || r.Gates[1].Name != "no new Pending pods" {
		t.Fatalf("verified %d, roll = %+v", rig.verified.Load(), r)
	}

	// Issues fail the roll's result, as exit 5 does for `nodegroup update`.
	rig = newRollRig(t)
	rig.verification = nodegroupsvc.PostRollVerification{Issues: []string{"2 pod(s) newly Pending after roll: [shop/checkout-1 shop/checkout-2]"}}
	if err := rig.start(t, roll); err != nil {
		t.Fatal(err)
	}
	rig.release <- ekstypes.UpdateStatusSuccessful
	rig.b.Close()
	st, _ = rig.b.State(t.Context())
	if r := st.Rolls[0]; !strings.HasPrefix(r.Failed, "post-roll verification: 2 pod(s) newly Pending") {
		t.Fatalf("roll Failed = %q", r.Failed)
	}

	// A failed roll is not verified.
	rig = newRollRig(t)
	if err := rig.start(t, roll); err != nil {
		t.Fatal(err)
	}
	rig.release <- ekstypes.UpdateStatusFailed
	rig.b.Close()
	if rig.verified.Load() != 0 {
		t.Fatal("a failed roll was verified")
	}
}

func TestUnofferedInstanceTypesWarnInTheDryRun(t *testing.T) {
	rig := newRollRig(t)
	rig.offerings = []nodegroupsvc.UnavailableOffering{{InstanceType: "m7i.large", AvailabilityZone: "us-east-1e"}}
	p, err := rig.b.Plan(t.Context(), roll)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, g := range p.Gates {
		if g.Text == "m7i.large not offered in us-east-1e" && g.Status == state.CheckWarn {
			found = true
		}
	}
	if !found || p.Blocked != "" {
		t.Fatalf("gates = %+v, blocked %q; the offering check warns, never blocks", p.Gates, p.Blocked)
	}
}

// A real roll's drain view showed counts only: the observer now names the
// pods left on a draining node, and the snapshot hands them to the view.
func TestRollSnapshotNamesTheDrainingPods(t *testing.T) {
	b := newTestBackend(t, &fleet{rows: map[string][]statussvc.ClusterStatus{}}, "us-east-1")
	r := &liveRoll{st: state.Roll{Snapshot: noderoll.Snapshot{Nodes: []noderoll.NodeView{
		{Name: "ip-old", Phase: noderoll.PhaseDraining, Pods: 2, PodsTotal: 3, PodList: []noderoll.PodView{
			{Name: "default/web-1"}, {Name: "kube-system/coredns-5d", Terminating: true},
		}},
		{Name: "ip-new", Phase: noderoll.PhaseReady, OnTarget: true},
	}}}}
	st := b.rollSnapshot(r)
	pods := st.Pods["ip-old"]
	if len(pods) != 2 || pods[0].Name != "default/web-1" || pods[0].State != state.PodRunning || pods[1].State != state.PodTerminating {
		t.Fatalf("pods = %+v", pods)
	}
	if _, ok := st.Pods["ip-new"]; ok {
		t.Fatal("a ready node got a drain list")
	}
	st.Snapshot.Nodes[0].PodList[0].Name = "changed"
	if r.st.Snapshot.Nodes[0].PodList[0].Name != "default/web-1" {
		t.Fatal("the snapshot shares the pod list with the roll")
	}
}

// A real `cluster upgrade` from the CLI showed nothing on the Rolls or
// Upgrade screens. The sweep now adopts changes started elsewhere: an
// UPDATING nodegroup gets a watched roll, an UPDATING cluster a watched
// upgrade; both are marked, cannot be controlled here, adopted once, and end
// when EKS says so.
func TestChangesStartedElsewhereAreWatched(t *testing.T) {
	rig := newRollRig(t)
	b := rig.b
	rows := prodRows() // ng-system is UPDATING
	rows[0].State = "UPDATING"
	b.svc.listStatuses = func(_ context.Context, cfg aws.Config, _ statussvc.ListOptions) ([]statussvc.ClusterStatus, error) {
		if cfg.Region != "us-east-1" {
			return nil, nil
		}
		return rows, nil
	}
	created := time.Date(2026, 9, 25, 11, 50, 0, 0, time.UTC)
	var looked atomic.Int64
	b.roll.findUpdate = func(_ context.Context, _ aws.Config, cluster, ng string) (*ekstypes.Update, error) {
		looked.Add(1)
		switch ng {
		case "ng-system":
			return &ekstypes.Update{Id: aws.String("u-ng"), Status: ekstypes.UpdateStatusInProgress, CreatedAt: &created,
				Params: []ekstypes.UpdateParam{{Type: ekstypes.UpdateParamTypeVersion, Value: aws.String("1.32")}}}, nil
		case "":
			return &ekstypes.Update{Id: aws.String("u-cp"), Type: ekstypes.UpdateTypeVersionUpdate, Status: ekstypes.UpdateStatusInProgress, CreatedAt: &created,
				Params: []ekstypes.UpdateParam{{Type: ekstypes.UpdateParamTypeVersion, Value: aws.String("1.32")}}}, nil
		}
		return nil, nil
	}
	cpDone := make(chan struct{})
	b.roll.waitCluster = func(ctx context.Context, _ aws.Config, _, _ string) (ekstypes.UpdateStatus, string, error) {
		select {
		case <-cpDone:
			return ekstypes.UpdateStatusSuccessful, "", nil
		case <-ctx.Done():
			return "", "", ctx.Err()
		}
	}
	b.mu.Lock()
	b.runCtx = t.Context()
	b.mu.Unlock()

	b.sweep(t.Context())
	var st state.State
	for {
		st, _ = b.State(t.Context())
		if len(st.Rolls) == 1 && len(st.Upgrades) == 1 {
			break
		}
		runtime.Gosched()
	}
	r, u := st.Rolls[0], st.Upgrades[0]
	if !r.StartedElsewhere || r.Nodegroup != "ng-system" || !r.StartedAt.Equal(created) || !u.StartedElsewhere || u.To != "1.32" || u.Phases[0].Status != state.PhaseRunning {
		t.Fatalf("roll %+v\nupgrade %+v", r, u)
	}
	if err := b.StopAfterCurrent(t.Context(), "prod-api"); err == nil || !strings.Contains(err.Error(), "started elsewhere") {
		t.Fatalf("StopAfterCurrent = %v", err)
	}
	// A second sweep while they run adopts nothing new.
	b.sweep(t.Context())
	if st, _ := b.State(t.Context()); len(st.Rolls) != 1 || len(st.Upgrades) != 1 {
		t.Fatalf("adopted twice: %d rolls, %d upgrades", len(st.Rolls), len(st.Upgrades))
	}

	rig.release <- ekstypes.UpdateStatusSuccessful
	close(cpDone)
	b.Close()
	st, _ = b.State(t.Context())
	if r := st.Rolls[0]; r.Running() || r.Failed != "" {
		t.Fatalf("roll after its update ended: %+v", r)
	}
	if u := st.Upgrades[0]; u.Running() || u.Phases[0].Status != state.PhaseDone || u.Phases[0].Items[0].Status != state.PhaseDone {
		t.Fatalf("upgrade after its update ended: %+v", u)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.adopting) != 0 || len(b.claimed) != 0 {
		t.Fatalf("left adopting %v, claimed %v", b.adopting, b.claimed)
	}
}

// From review: a watched roll that ended must not release a claim someone
// else holds, and a second adoption of the same update (after a watch that
// ended early while EKS still reports UPDATING) resumes the same roll.
func TestAdoptedRollResumesAndKeepsOthersClaims(t *testing.T) {
	rig := newRollRig(t)
	b := rig.b
	rows := prodRows() // ng-system is UPDATING
	b.svc.listStatuses = func(_ context.Context, cfg aws.Config, _ statussvc.ListOptions) ([]statussvc.ClusterStatus, error) {
		if cfg.Region != "us-east-1" {
			return nil, nil
		}
		return rows, nil
	}
	b.roll.findUpdate = func(_ context.Context, _ aws.Config, _, ng string) (*ekstypes.Update, error) {
		if ng != "ng-system" {
			return nil, nil
		}
		return &ekstypes.Update{Id: aws.String("u-ng"), Status: ekstypes.UpdateStatusInProgress}, nil
	}
	b.mu.Lock()
	b.runCtx = t.Context()
	b.mu.Unlock()
	rolls := func() []state.Roll { st, _ := b.State(t.Context()); return st.Rolls }
	waitFor := func(ok func([]state.Roll) bool) {
		for !ok(rolls()) {
			runtime.Gosched()
		}
	}

	b.sweep(t.Context())
	waitFor(func(rs []state.Roll) bool { return len(rs) == 1 && rs[0].Running() })
	b.mu.Lock()
	tgt := b.targets["prod-api"]
	b.claimed[tgt] = "upgrading" // an upgrade started here since
	b.mu.Unlock()

	rig.release <- ekstypes.UpdateStatusInProgress // the watch ends early
	waitFor(func(rs []state.Roll) bool { return !rs[0].Running() })
	b.sweep(t.Context()) // EKS still reports UPDATING
	waitFor(func(rs []state.Roll) bool { return len(rs) == 1 && rs[0].Running() })

	rig.release <- ekstypes.UpdateStatusSuccessful
	b.Close()
	if rs := rolls(); len(rs) != 1 || rs[0].Running() || rs[0].Failed != "" {
		t.Fatalf("rolls = %+v", rs)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.claimed[tgt] != "upgrading" {
		t.Fatalf("the watched roll released another change's claim: %q", b.claimed[tgt])
	}
}

// A dry run that recorded no version (no sweep version, and EKS could not
// be read) cannot vouch for what Start would roll: Start refuses.
func TestARollWithNoRecordedVersionRefuses(t *testing.T) {
	rig := newRollRig(t)
	rig.b.mu.Lock()
	rig.b.accepted[acceptKey(rig.b.targets["prod-api"], "ng-general")] = acceptedRoll{}
	rig.b.mu.Unlock()
	if err := rig.b.Start(t.Context(), roll); err == nil || !strings.Contains(err.Error(), "could not read the version of ng-general") || rig.started.Load() != 0 {
		t.Fatalf("Start = %v, want a refusal and no roll", err)
	}
	rig.b.Close()
}

func TestShownVersionIsTheDryRunsCluster(t *testing.T) {
	c := state.Cluster{Name: "prod-api", Nodegroups: []state.Nodegroup{{Name: "ng-a", Version: "1.31"}, {Name: "ng-b", Version: "1.30"}}}
	if got := shownVersion(c, "ng-b"); got != "1.30" {
		t.Errorf("shownVersion = %q, want 1.30", got)
	}
	if got := shownVersion(c, "ng-x"); got != "" {
		t.Errorf("shownVersion of a missing nodegroup = %q, want empty", got)
	}
}
