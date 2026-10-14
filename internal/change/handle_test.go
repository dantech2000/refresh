package change

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/dantech2000/refresh/internal/fleet"
)

// scripted is a run that the test writes step by step: each op runs on
// the goroutine of the run with its hooks, as the executor would call
// them. Closing ops ends the run with result.
type scripted struct {
	ops    chan func(Hooks)
	result Result
}

func newScripted() *scripted { return &scripted{ops: make(chan func(Hooks))} }

func (s *scripted) run(h Hooks) (Result, error) {
	for op := range s.ops {
		op(h)
	}
	return s.result, nil
}

// do runs f on the run's goroutine and returns when it has run.
func (s *scripted) do(f func(Hooks)) {
	done := make(chan struct{})
	s.ops <- func(h Hooks) { f(h); close(done) }
	<-done
}

// end ends the run with result.
func (s *scripted) end(result Result) {
	s.result = result
	close(s.ops)
}

var (
	handleTarget = Target{Region: east, Cluster: "prod"}
	handleNow    = time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
)

func handleClock() time.Time { return handleNow }

// twoPhasePlan is an upgrade plan with a control-plane phase and a serial
// nodegroups phase of two rolls: three checkpoints, the last before ng-b.
func twoPhasePlan() *Plan {
	cp := PhaseRef{Target: handleTarget, Kind: PhaseControlPlane, Hop: fleet.Version{Major: 1, Minor: 34}}
	ng := PhaseRef{Target: handleTarget, Kind: PhaseNodegroups, Hop: fleet.Version{Major: 1, Minor: 34}}
	return &Plan{Kind: ClusterUpgrade, Clusters: []ClusterPlan{{Target: handleTarget, Phases: []Phase{
		{Ref: cp, Steps: []Step{{Ref: StepRef{Phase: cp, Kind: StepControlPlane}, Action: Submit, From: "1.33", To: "1.34"}}},
		{Ref: ng, Steps: []Step{
			{Ref: StepRef{Phase: ng, Kind: StepNodegroup, Name: "ng-a"}, Action: Submit, From: "1.33", To: "1.34"},
			{Ref: StepRef{Phase: ng, Kind: StepNodegroup, Name: "ng-b"}, Action: Submit, From: "1.33", To: "1.34"},
		}},
	}}}, At: handleNow}
}

// wantState waits until every goroutine is idle and asserts the state.
func wantState(t *testing.T, h *Handle, want RunState) {
	t.Helper()
	synctest.Wait()
	if got := h.View().State; got != want {
		t.Fatalf("state = %s, want %s", got, want)
	}
}

// askAsync makes the run ask q and returns the channel of the answer.
func askAsync(s *scripted, q Question) <-chan bool {
	out := make(chan bool, 1)
	s.ops <- func(h Hooks) {
		yes, _ := h.Ask(context.Background(), q)
		out <- yes
	}
	return out
}

// checkpointAsync makes the run call the checkpoint before next and
// returns the channel of the answer.
func checkpointAsync(s *scripted, next StepRef) <-chan Checkpoint {
	out := make(chan Checkpoint, 1)
	s.ops <- func(h Hooks) {
		cp, _ := h.Checkpoint(context.Background(), next)
		out <- cp
	}
	return out
}

// The state table: a question, a pause, a resume, a stop at a checkpoint,
// and the end, each with the controls it allows.
func TestHandle_StateTable(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := newScripted()
		h := start(ClusterUpgrade, handleTarget, handleClock, true, s.run)
		wantState(t, h, Preparing)
		plan := twoPhasePlan()
		s.do(func(hk Hooks) { hk.Event(Event{Kind: PlanReady, Plan: plan}) })
		if v := h.View(); len(v.Steps) != 3 || len(v.Phases) != 2 || !v.Controls.Stop || !v.Controls.Pause || v.Controls.Answer {
			t.Fatalf("after the plan: %d steps, %d phases, controls %+v", len(v.Steps), len(v.Phases), v.Controls)
		}

		// A question before the start: Preparing to Asking and back.
		answered := askAsync(s, Question{Kind: ConfirmStart, Text: "Upgrade prod?"})
		wantState(t, h, Asking)
		q := h.View().Question
		if q == nil || q.ID != 1 || !h.View().Controls.Answer {
			t.Fatalf("no open question: %+v", h.View())
		}
		if err := h.Answer(q.ID, true); err != nil {
			t.Fatal(err)
		}
		if yes := <-answered; !yes {
			t.Fatal("the answer yes did not reach the run")
		}
		wantState(t, h, Preparing)
		if err := h.Answer(q.ID, true); !errors.Is(err, ErrStaleQuestion) {
			t.Fatalf("a second answer = %v, want ErrStaleQuestion", err)
		}

		// A checkpoint with nothing asked goes on; the phase then runs.
		if cp := <-checkpointAsync(s, StepRef{Phase: plan.Clusters[0].Phases[0].Ref}); cp.Stop || cp.Waited {
			t.Fatalf("the first checkpoint = %+v, want go on", cp)
		}
		s.do(func(hk Hooks) { hk.Event(Event{Kind: PhaseStarted, Phase: plan.Clusters[0].Phases[0].Ref, At: handleNow}) })
		wantState(t, h, Running)

		// A pause holds the next checkpoint; a resume lets it go.
		if err := h.Pause(); err != nil {
			t.Fatal(err)
		}
		next := plan.Clusters[0].Phases[1].Ref
		held := checkpointAsync(s, StepRef{Phase: next})
		wantState(t, h, Paused)
		if err := h.Resume(); err != nil {
			t.Fatal(err)
		}
		if cp := <-held; cp.Stop || !cp.Waited {
			t.Fatalf("the checkpoint after a resume = %+v, want Waited", cp)
		}
		wantState(t, h, Running)
		if err := h.Resume(); !errors.Is(err, ErrNotPaused) {
			t.Fatalf("Resume when not paused = %v", err)
		}

		// A stop ends the run at the next checkpoint: the step before it is
		// done, the rest is not reached.
		if err := h.Stop(); err != nil {
			t.Fatal(err)
		}
		if cp := <-checkpointAsync(s, plan.Clusters[0].Phases[1].Steps[1].Ref); !cp.Stop {
			t.Fatalf("the checkpoint after a stop = %+v, want Stop", cp)
		}
		s.end(Result{Status: Stopped})
		<-h.Done()
		v := h.View()
		if v.State != Finished || v.Result == nil || v.Result.Status != Stopped || v.Controls != (Controls{}) {
			t.Fatalf("after the end: %+v", v)
		}
		if v.Phases[1].Status != PhaseStopped {
			t.Fatalf("the phase a stop cut short is %s, want Stopped", v.Phases[1].Status)
		}
		for _, call := range []func() error{h.Pause, h.Resume, h.Stop, func() error { return h.Answer(1, true) }} {
			if err := call(); !errors.Is(err, ErrFinished) {
				t.Errorf("a control after the end = %v, want ErrFinished", err)
			}
		}
	})
}

// A stop before the first checkpoint ends the run with nothing submitted.
func TestHandle_StopBeforeTheFirstCheckpoint(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := newScripted()
		h := start(ClusterUpgrade, handleTarget, handleClock, true, s.run)
		s.do(func(hk Hooks) { hk.Event(Event{Kind: PlanReady, Plan: twoPhasePlan()}) })
		if err := h.Stop(); err != nil {
			t.Fatal(err)
		}
		if cp := <-checkpointAsync(s, StepRef{Phase: twoPhasePlan().Clusters[0].Phases[0].Ref}); !cp.Stop {
			t.Fatalf("the first checkpoint = %+v, want Stop", cp)
		}
		submitted := false
		// The run honours the stop: it ends without the submit op.
		s.end(Result{Status: Stopped})
		<-h.Done()
		if submitted || h.View().Steps[0].Status != StepNotAttempted {
			t.Fatal("something was submitted after a stop before the first checkpoint")
		}
	})
}

// After the last checkpoint of the plan a stop and a pause are too late.
func TestHandle_StopAfterTheLastCheckpointIsTooLate(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := newScripted()
		h := start(ClusterUpgrade, handleTarget, handleClock, true, s.run)
		plan := twoPhasePlan()
		s.do(func(hk Hooks) { hk.Event(Event{Kind: PlanReady, Plan: plan}) })
		last := plan.Clusters[0].Phases[1].Steps[1].Ref
		<-checkpointAsync(s, StepRef{Phase: plan.Clusters[0].Phases[0].Ref})
		if !h.View().Controls.Stop {
			t.Fatal("a stop is not offered between checkpoints")
		}
		<-checkpointAsync(s, last)
		if v := h.View(); v.Controls.Stop || v.Controls.Pause {
			t.Fatalf("controls after the last checkpoint = %+v", v.Controls)
		}
		if err := h.Stop(); !errors.Is(err, ErrTooLate) {
			t.Fatalf("Stop after the last checkpoint = %v, want ErrTooLate", err)
		}
		if err := h.Pause(); !errors.Is(err, ErrTooLate) {
			t.Fatalf("Pause after the last checkpoint = %v, want ErrTooLate", err)
		}
		s.end(Result{Status: Succeeded})
		<-h.Done()
	})
}

// A kind with no checkpoints has no point where a stop can take effect.
func TestHandle_NoCheckpoints(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := newScripted()
		h := start(AddonUpdate, handleTarget, handleClock, false, s.run)
		if v := h.View(); v.Controls.Stop || v.Controls.Pause {
			t.Fatalf("controls of an add-on update = %+v", v.Controls)
		}
		if err := h.Stop(); !errors.Is(err, ErrTooLate) {
			t.Fatalf("Stop = %v, want ErrTooLate", err)
		}
		s.end(Result{Status: Succeeded})
		<-h.Done()
	})
}

// Exactly one of two answers wins, whatever the interleaving.
func TestHandle_OneAnswerWins(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := newScripted()
		h := start(ClusterUpgrade, handleTarget, handleClock, true, s.run)
		answered := askAsync(s, Question{Kind: GateWarnings, Text: "Go on?"})
		wantState(t, h, Asking)
		id := h.View().Question.ID
		var wg sync.WaitGroup
		errs := make(chan error, 2)
		for _, yes := range []bool{true, false} {
			wg.Go(func() { errs <- h.Answer(id, yes) })
		}
		wg.Wait()
		close(errs)
		won, stale := 0, 0
		for err := range errs {
			switch {
			case err == nil:
				won++
			case errors.Is(err, ErrStaleQuestion):
				stale++
			default:
				t.Fatal(err)
			}
		}
		if won != 1 || stale != 1 {
			t.Fatalf("%d answers won and %d were stale, want 1 and 1", won, stale)
		}
		<-answered
		s.end(Result{})
		<-h.Done()
	})
}

// A stop while a question is open answers it with no; a stop while paused
// ends the pause with the stop.
func TestHandle_StopWhileAskingOrPaused(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := newScripted()
		h := start(ClusterUpgrade, handleTarget, handleClock, true, s.run)
		answered := askAsync(s, Question{Kind: PlanChanged, Text: "Changed. Continue?"})
		wantState(t, h, Asking)
		if err := h.Stop(); err != nil {
			t.Fatal(err)
		}
		if yes := <-answered; yes {
			t.Fatal("a stop answered yes")
		}
		if v := h.View(); v.Question != nil || !v.StopRequested {
			t.Fatalf("after a stop while asking: %+v", v)
		}
		s.end(Result{Status: Declined})
		<-h.Done()

		s = newScripted()
		h = start(ClusterUpgrade, handleTarget, handleClock, true, s.run)
		if err := h.Pause(); err != nil {
			t.Fatal(err)
		}
		held := checkpointAsync(s, StepRef{})
		wantState(t, h, Paused)
		if err := h.Stop(); err != nil {
			t.Fatal(err)
		}
		if cp := <-held; !cp.Stop || !cp.Waited {
			t.Fatalf("the checkpoint of a stop while paused = %+v", cp)
		}
		s.end(Result{Status: Stopped})
		<-h.Done()
	})
}

// A cancelled run ends a question and a pause with the error of the
// context, and the handle reports the state before them.
func TestHandle_CancelWhileAskingOrPaused(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := newScripted()
		h := start(ClusterUpgrade, handleTarget, handleClock, true, s.run)
		ctx, cancel := context.WithCancel(context.Background())
		errs := make(chan error, 1)
		s.ops <- func(hk Hooks) {
			_, err := hk.Ask(ctx, Question{Kind: ConfirmStart})
			errs <- err
		}
		wantState(t, h, Asking)
		cancel()
		if err := <-errs; !errors.Is(err, context.Canceled) {
			t.Fatalf("Ask after cancel = %v", err)
		}
		wantState(t, h, Preparing)

		if err := h.Pause(); err != nil {
			t.Fatal(err)
		}
		ctx, cancel = context.WithCancel(context.Background())
		s.ops <- func(hk Hooks) {
			_, err := hk.Checkpoint(ctx, StepRef{})
			errs <- err
		}
		wantState(t, h, Paused)
		cancel()
		if err := <-errs; !errors.Is(err, context.Canceled) {
			t.Fatalf("Checkpoint after cancel = %v", err)
		}
		s.end(Result{Status: Interrupted})
		<-h.Done()
	})
}

// The progress kinds replace the progress of their step and are not
// logged; the log keeps every entry since the newest plan, and drops the
// older ones past its cap.
func TestHandle_EventsAndProgress(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := newScripted()
		h := start(NodegroupUpdate, handleTarget, handleClock, false, s.run)
		plan := twoPhasePlan()
		step := plan.Clusters[0].Phases[1].Steps[0].Ref
		s.do(func(hk Hooks) {
			hk.Event(Event{Kind: PlanReady, Plan: plan})
			for i := range 10 {
				hk.Event(Event{Kind: Notice, Text: fmt.Sprint("old ", i)})
			}
			for i := range 3 {
				hk.Event(Event{Kind: UpdatePolled, Step: step, Update: &UpdateProgress{Status: fmt.Sprint("poll ", i)}})
				hk.Event(Event{Kind: NodeProgress, Step: step, Nodes: &NodeView{Nodegroup: "ng-a"}})
			}
		})
		v := h.View()
		if v.LastSeq != 11 || v.Progress[step].Update.Status != "poll 2" || v.Progress[step].Nodes == nil {
			t.Fatalf("after 11 lifecycle events and 6 progress events: LastSeq %d, progress %+v", v.LastSeq, v.Progress[step])
		}
		entries := h.Events(5)
		if len(entries) != 6 || entries[0].Seq != 6 {
			t.Fatalf("Events(5) = %d entries from %d, want 6 from 6", len(entries), entries[0].Seq)
		}
		s.do(func(hk Hooks) {
			hk.Event(Event{Kind: PlanReady, Plan: plan})
			for i := range maxLog + 100 {
				hk.Event(Event{Kind: Notice, Text: fmt.Sprint("new ", i)})
			}
		})
		v = h.View()
		entries = h.Events(0)
		if v.Dropped != 11 || len(entries) != maxLog+101 || entries[0].Kind != PlanReady {
			t.Fatalf("after the cap: dropped %d, %d entries, first %s; want 11, %d, PlanReady", v.Dropped, len(entries), entries[0].Kind, maxLog+101)
		}
		s.end(Result{})
		<-h.Done()
	})
}

// A view shares nothing that a reader can change in the handle, and the
// result that Run emits is copied.
func TestHandle_ViewIsImmutable(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := newScripted()
		h := start(NodegroupUpdate, handleTarget, handleClock, false, s.run)
		plan := twoPhasePlan()
		res := &StepResult{Ref: plan.Clusters[0].Phases[0].Steps[0].Ref, Status: StepStarted, UpdateID: "u1"}
		s.do(func(hk Hooks) {
			hk.Event(Event{Kind: PlanReady, Plan: plan})
			hk.Event(Event{Kind: StepChanged, Step: res.Ref, Result: res})
		})
		res.Status = StepFailed // Run writes on after it emitted
		v := h.View()
		v.Steps[0].Status = StepCancelled
		v.Phases[0].Status = PhaseFailed
		again := h.View()
		if again.Steps[0].Status != StepStarted || again.Phases[0].Status != PhasePending {
			t.Fatalf("a view changed the handle: %+v", again.Steps[0])
		}
		s.end(Result{})
		<-h.Done()
	})
}

// The phases follow their events: skipped, done, failed, and stopped at
// the end for one that never started.
func TestHandle_PhaseStates(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := newScripted()
		h := start(ClusterUpgrade, handleTarget, handleClock, true, s.run)
		plan := twoPhasePlan()
		cp, ng := plan.Clusters[0].Phases[0], plan.Clusters[0].Phases[1]
		s.do(func(hk Hooks) {
			hk.Event(Event{Kind: PlanReady, Plan: plan})
			hk.Event(Event{Kind: PhaseStarted, Phase: cp.Ref})
			hk.Event(Event{Kind: PhaseEnded, Phase: cp.Ref, Skipped: true})
			hk.Event(Event{Kind: PhaseStarted, Phase: ng.Ref})
			hk.Event(Event{Kind: StepChanged, Step: ng.Steps[0].Ref, Result: &StepResult{Ref: ng.Steps[0].Ref, Status: StepFailed}})
		})
		if v := h.View(); v.Phases[0].Status != PhaseSkipped || v.Phases[1].Status != PhaseRunning {
			t.Fatalf("phases = %+v", v.Phases)
		}
		s.do(func(hk Hooks) { hk.Event(Event{Kind: PhaseEnded, Phase: ng.Ref}) })
		if v := h.View(); v.Phases[1].Status != PhaseFailed {
			t.Fatalf("a phase with a failed step is %s, want Failed", v.Phases[1].Status)
		}
		s.end(Result{Status: Failed})
		<-h.Done()
	})
}

// A handle of a change started elsewhere refuses every control.
func TestHandle_ElsewhereHasNoControls(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s := newScripted()
		h := newHandle(NodegroupUpdate, handleTarget, true, handleClock)
		go h.run(s.run)
		answered := askAsync(s, Question{Kind: GateWarnings})
		wantState(t, h, Asking)
		id := h.View().Question.ID
		for name, call := range map[string]func() error{"Answer": func() error { return h.Answer(id, true) }, "Pause": h.Pause, "Resume": h.Resume, "Stop": h.Stop} {
			if err := call(); !errors.Is(err, ErrNoControls) {
				t.Errorf("%s on a watched change = %v, want ErrNoControls", name, err)
			}
		}
		if h.View().Controls != (Controls{}) {
			t.Fatal("a watched change offers controls")
		}
		// Only the run itself can end its question.
		s.end(Result{})
		h.mu.Lock()
		_ = h.resolve(id, answeredNo)
		h.mu.Unlock()
		<-answered
		<-h.Done()
	})
}
