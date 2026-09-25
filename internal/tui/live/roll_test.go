package live

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/dantech2000/refresh/internal/health"
	"github.com/dantech2000/refresh/internal/noderoll"
	statussvc "github.com/dantech2000/refresh/internal/services/status"
	"github.com/dantech2000/refresh/internal/tui/state"
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
	release chan ekstypes.UpdateStatus
	atEnd   chan struct{}
}

func newRollRig(t *testing.T) *rollRig {
	t.Helper()
	rows := prodRows()
	rows[0].Nodegroups[1].Status = "ACTIVE" // nothing in flight
	f := &fleet{rows: map[string][]statussvc.ClusterStatus{"us-east-1": rows}}
	rig := &rollRig{b: newTestBackend(t, f, "us-east-1"), health: health.DecisionProceed,
		release: make(chan ekstypes.UpdateStatus, 1), atEnd: make(chan struct{}, 1)}
	b := rig.b
	b.opts.AllowChanges = true
	b.opts.ObserveInterval = time.Millisecond
	b.roll = rollServices{
		kubeFor: func(context.Context, aws.Config, string) (kubernetes.Interface, string) {
			return fake.NewClientset(), "kubeconfig context prod-api"
		},
		healthCheck: func(context.Context, aws.Config, string, []string, kubernetes.Interface) health.HealthSummary {
			s := health.HealthSummary{Decision: rig.health, Results: []health.HealthResult{{Name: "nodes Ready", Status: health.StatusPass}}}
			if rig.health == health.DecisionBlock {
				s.Results = append(s.Results, health.HealthResult{Name: "PDB drain blockers", Status: health.StatusFail, IsBlocking: true, Message: "checkout allows 0 disruptions"})
			}
			return s
		},
		startRoll: func(_ context.Context, _ aws.Config, cluster, ng, version string) (*ekstypes.Update, error) {
			rig.started.Add(1)
			rig.version.Store(cluster + "/" + ng + "@" + version)
			return &ekstypes.Update{Id: aws.String("upd-1")}, nil
		},
		waitUpdate: func(ctx context.Context, _ aws.Config, _, _, _ string, _ time.Duration) (ekstypes.UpdateStatus, string, error) {
			select {
			case s := <-rig.release:
				if s == ekstypes.UpdateStatusInProgress {
					return s, "", errors.New("monitoring timeout reached")
				}
				return s, "", nil
			case <-ctx.Done():
				return ekstypes.UpdateStatusInProgress, "", ctx.Err()
			}
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
	if r.Running() || r.Failed != "" || r.Replaced() != 3 {
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
	err := rig.b.Start(t.Context(), roll)
	if err == nil || rig.started.Load() != 0 {
		t.Fatalf("Start = %v with %d rolls started; the gate must stop it before any change", err, rig.started.Load())
	}
	// The claim is released: once the gate passes, the roll can start.
	rig.health = health.DecisionProceed
	if err := rig.b.Start(t.Context(), roll); err != nil {
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
		"failed":  {ekstypes.UpdateStatusFailed, "Failed"},
		"timeout": {ekstypes.UpdateStatusInProgress, "outcome unknown"},
	} {
		t.Run(name, func(t *testing.T) {
			rig := newRollRig(t)
			if err := rig.b.Start(t.Context(), roll); err != nil {
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
	if err := rig.b.Start(t.Context(), roll); err == nil || !strings.Contains(err.Error(), "InvalidRequestException") {
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

func TestOnlyRollsStartEvenWithChangesOn(t *testing.T) {
	rig := newRollRig(t)
	for _, a := range []state.Action{
		{Kind: state.ActionAddons, Cluster: "prod-api"},
		{Kind: state.ActionUpgrade, Cluster: "prod-api"},
	} {
		if err := rig.b.Start(t.Context(), a); err == nil || !strings.Contains(err.Error(), "only nodegroup rolls") {
			t.Fatalf("Start(%v) = %v", a.Kind, err)
		}
	}
	current := state.Action{Kind: state.ActionRoll, Cluster: "prod-api", Nodegroup: "ng-system"}
	if err := rig.b.Start(t.Context(), current); err == nil || rig.started.Load() != 0 {
		t.Fatalf("a roll of a current nodegroup started: %v", err)
	}
}

func TestNoKubeAccessStillRollsWithoutTheNodeView(t *testing.T) {
	rig := newRollRig(t)
	rig.b.roll.kubeFor = func(context.Context, aws.Config, string) (kubernetes.Interface, string) {
		return nil, "no kubeconfig context for this cluster · add one: aws eks update-kubeconfig --name prod-api --region us-east-1"
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
