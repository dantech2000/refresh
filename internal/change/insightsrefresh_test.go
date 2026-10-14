package change

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/dantech2000/refresh/internal/apidoc"
	"github.com/dantech2000/refresh/internal/awsx"
	"github.com/dantech2000/refresh/internal/exitcode"
	"github.com/dantech2000/refresh/internal/fleet/fleettest"
)

// refuseStart makes EKS refuse StartInsightsRefresh with a code.
func refuseStart(w *fleettest.World, code string) {
	w.SetFault(fleettest.EKSStartInsightsRefresh, fleettest.Fault{Code: code})
}

// hideRefresh makes DescribeInsightsRefresh answer not found, as EKS does
// while a scheduled refresh runs.
func hideRefresh(w *fleettest.World) {
	w.SetFault(fleettest.EKSDescribeInsightsRefresh, fleettest.Fault{Code: codeNotFound})
}

// progressOf returns hooks that collect the progress lines of a run.
func progressOf(lines *[]string) Hooks {
	return Hooks{Event: func(ev Event) {
		if ev.Kind == Progress {
			*lines = append(*lines, ev.Text)
		}
	}}
}

// wantRefreshBlocked checks that the refresh blocked the run before any
// change: one Unread blocker with the words, exit code 4, and no request
// other than the refresh. It returns the message of the blocker and the
// failure of the run, which names the call that failed.
func wantRefreshBlocked(t *testing.T, e *env, res Result, words ...string) (string, apidoc.Failure) {
	t.Helper()
	if res.Plan == nil || len(res.Plan.Clusters) != 1 || len(res.Plan.Clusters[0].Blockers) != 1 {
		t.Fatalf("result = %s, err %v, plan %+v; want a plan with one blocker", res.Status, res.Err, res.Plan)
	}
	b := res.Plan.Clusters[0].Blockers[0]
	if b.Kind != BlockerUnread || !strings.HasPrefix(b.Message, "could not refresh cluster insights for 1.33") {
		t.Errorf("blocker = %s %q; want Unread about the refresh for 1.33", b.Kind, b.Message)
	}
	for _, w := range words {
		if !strings.Contains(b.Message, w) {
			t.Errorf("the blocker lacks %q: %s", w, b.Message)
		}
	}
	if res.Status != Refused || res.ExitCode() != exitcode.Incomplete {
		t.Errorf("status = %s, exit %d; want Refused, exit 4", res.Status, res.ExitCode())
	}
	for _, r := range e.sent() {
		if r != sentRefresh {
			t.Errorf("%s was sent by a run that the refresh blocked (%v)", r, e.sent())
		}
	}
	if len(res.Failures) != 1 || res.Failures[0].Kind != apidoc.FailureCluster || res.Failures[0].Name != "prod" {
		t.Fatalf("failures = %+v; want one, of the cluster prod", res.Failures)
	}
	return b.Message, res.Failures[0]
}

// What EKS did on a real cluster during a scheduled refresh: the start is
// refused with InvalidRequestException and the describe says not found.
// The run waits and starts again. It does not block.
func TestInsightsRefresh_WaitsOutAScheduledRefresh(t *testing.T) {
	world := engineWorld(nil)
	e := newEnv(t, world)
	refuseStart(world, codeInvalidRequest)
	hideRefresh(world)
	// The scheduled refresh ends before the third poll.
	e.onRequest(fleettest.EKSDescribeInsightsRefresh, func(n int) {
		if n == 3 {
			world.SetFault(fleettest.EKSStartInsightsRefresh, fleettest.Fault{})
			world.SetFault(fleettest.EKSDescribeInsightsRefresh, fleettest.Fault{})
		}
	})
	var lines []string
	res := mustRun(t, e, upgradeTo("1.33"), progressOf(&lines))
	if res.Status != Succeeded || res.ExitCode() != exitcode.OK {
		t.Fatalf("status = %s, exit %d, err %v; want Succeeded", res.Status, res.ExitCode(), res.Err)
	}
	// The start is sent again on each poll until EKS accepts it: three
	// that are refused while the scheduled refresh runs, then the one
	// that starts. The answer of the third poll is the end of the
	// scheduled refresh, which is not a refresh of the run.
	want := append([]string{sentRefresh, sentRefresh, sentRefresh}, wholeUpgrade()...)
	if diff := cmp.Diff(want, e.sent()); diff != "" {
		t.Errorf("requests (-want +got):\n%s", diff)
	}
	said := 0
	for _, l := range lines {
		if strings.Contains(l, "scheduled insights refresh") {
			said++
		}
	}
	if said != 1 {
		t.Errorf("%d progress lines about the scheduled refresh, want 1: %q", said, lines)
	}
}

// A start that is refused because a refresh runs already follows that
// refresh, and sends no second start.
func TestInsightsRefresh_FollowsARefreshAlreadyRunning(t *testing.T) {
	world := engineWorld(func(s *fleettest.State, _ *fleettest.Cluster) { s.InsightsRefresh = time.Minute })
	e := newEnv(t, world)
	if err := world.StartInsightsRefresh(east, "prod"); err != nil {
		t.Fatal(err)
	}
	refuseStart(world, codeInvalidRequest)
	var lines []string
	res := mustRun(t, e, upgradeTo("1.33"), progressOf(&lines))
	if res.Status != Succeeded || res.ExitCode() != exitcode.OK {
		t.Fatalf("status = %s, exit %d, err %v; want Succeeded", res.Status, res.ExitCode(), res.Err)
	}
	if diff := cmp.Diff(wholeUpgrade(), e.sent()); diff != "" {
		t.Errorf("requests (-want +got):\n%s", diff)
	}
	if !slices.ContainsFunc(lines, func(l string) bool { return strings.Contains(l, "already running for prod") }) {
		t.Errorf("no progress line says that a refresh is already running: %q", lines)
	}
	// The control plane moved only after the adopted refresh completed.
	if world.Now().Sub(fleettest.New().Now()) < time.Minute {
		t.Errorf("the run took %s, less than the refresh that it had to follow", world.Now().Sub(fleettest.New().Now()))
	}
}

// refreshOnce runs one insights refresh of prod, as the plan of an upgrade
// does, and returns its error.
func refreshOnce(ctx context.Context, e *env) error {
	ir := &insightsRefresher{d: e.deps, poll: 15 * time.Second}
	return ir.refresh(ctx, Target{Region: east, Cluster: "prod"}, "1.32")
}

// A refresh that shows up during the wait is followed, and it clears the
// refusal: when it does not end in time, the error is the timeout and not
// the refused start.
func TestInsightsRefresh_AdoptedRefreshClearsTheRefusal(t *testing.T) {
	world := engineWorld(func(s *fleettest.State, _ *fleettest.Cluster) { s.InsightsRefresh = time.Hour })
	e := newEnv(t, world)
	refuseStart(world, codeInvalidRequest)
	hideRefresh(world)
	e.onRequest(fleettest.EKSDescribeInsightsRefresh, func(n int) {
		if n == 2 {
			world.SetFault(fleettest.EKSDescribeInsightsRefresh, fleettest.Fault{})
			if err := world.StartInsightsRefresh(east, "prod"); err != nil {
				t.Error(err)
			}
		}
	})
	err := refreshOnce(testContext(t), e)
	if !errors.Is(err, errInsightsRefreshTimeout) || strings.Contains(err.Error(), "refused") || awsx.ErrorCode(err) != "" {
		t.Errorf("err = %v; want the timeout, without the refusal that the adopted refresh cleared", err)
	}
	if got := world.Now().Sub(fleettest.New().Now()); got != insightsRefreshLimit {
		t.Errorf("the refresh waited %s, want %s", got, insightsRefreshLimit)
	}
}

// The words of the refresher for the starts that EKS refuses.
func TestInsightsRefresh_ErrorText(t *testing.T) {
	t.Run("still refused when the wait ends", func(t *testing.T) {
		e := newEnv(t, engineWorld(nil))
		refuseStart(e.world, codeInvalidRequest)
		hideRefresh(e.world)
		err := refreshOnce(testContext(t), e)
		if err == nil || !strings.Contains(err.Error(), "still refused after 5m0s") || awsx.ErrorCode(err) != codeInvalidRequest {
			t.Errorf("err = %v; want the refusal, and that it lasted the whole wait", err)
		}
		if errors.Is(err, errInsightsRefreshTimeout) {
			t.Errorf("err = %v; a refused start is not only a timeout", err)
		}
	})
	t.Run("a new cluster", func(t *testing.T) {
		e := newEnv(t, engineWorld(nil))
		refuseStart(e.world, codeNotFound)
		err := refreshOnce(testContext(t), e)
		if err == nil || !strings.Contains(err.Error(), "has not made cluster insights available for prod yet") || !strings.Contains(err.Error(), "run again later") {
			t.Errorf("err = %v; want it to say that the cluster is new and when to run again", err)
		}
		if n := e.count(fleettest.EKSStartInsightsRefresh); n != 1 {
			t.Errorf("%d starts, want 1", n)
		}
	})
	t.Run("the context of the caller ends", func(t *testing.T) {
		world := engineWorld(func(s *fleettest.State, _ *fleettest.Cluster) { s.InsightsRefresh = time.Hour })
		e := newEnv(t, world)
		ctx, cancel := context.WithCancel(testContext(t))
		defer cancel()
		e.onRequest(fleettest.EKSDescribeInsightsRefresh, func(n int) {
			if n == 2 {
				cancel()
			}
		})
		err := refreshOnce(ctx, e)
		if !errors.Is(err, context.Canceled) || errors.Is(err, errInsightsRefreshTimeout) {
			t.Errorf("err = %v; want the cancelled context, and not a timeout", err)
		}
	})
}

// InvalidRequestException also means a cluster state such as DELETING. A
// start that is still refused when the wait ends is named in the blocker,
// which is then more than a timeout.
func TestInsightsRefresh_StillRefusedNamesTheRefusal(t *testing.T) {
	world := engineWorld(nil)
	e := newEnv(t, world)
	refuseStart(world, codeInvalidRequest)
	hideRefresh(world)
	res := mustRun(t, e, upgradeTo("1.33"), Hooks{})
	msg, f := wantRefreshBlocked(t, e, res, codeInvalidRequest)
	if f.Operation != string(fleettest.EKSStartInsightsRefresh) {
		t.Errorf("the failure names the call %q, want the refused start", f.Operation)
	}
	if strings.Contains(msg, "did not finish in time") {
		t.Errorf("the blocker is only a timeout: %s", msg)
	}
	if n := e.count(fleettest.EKSListInsights); n != 0 {
		t.Errorf("%d reads of the insights after a refresh that never started, want none", n)
	}
}

// The refusal that the blocker names is the latest one. A start that is
// first refused as during a scheduled refresh and then denied reports the
// denial.
func TestInsightsRefresh_LatestRefusalIsNamed(t *testing.T) {
	world := engineWorld(nil)
	e := newEnv(t, world)
	refuseStart(world, codeInvalidRequest)
	hideRefresh(world)
	e.onRequest(fleettest.EKSStartInsightsRefresh, func(n int) {
		if n == 2 {
			world.SetFault(fleettest.EKSStartInsightsRefresh, fleettest.Fault{Deny: true})
		}
	})
	res := mustRun(t, e, upgradeTo("1.33"), Hooks{})
	msg, f := wantRefreshBlocked(t, e, res, "AccessDenied")
	if strings.Contains(msg, codeInvalidRequest) || strings.Contains(msg, "still refused") {
		t.Errorf("the blocker names the earlier refusal: %s", msg)
	}
	if f.Reason != apidoc.ReasonAccessDenied || f.Operation != string(fleettest.EKSStartInsightsRefresh) {
		t.Errorf("failure = %s of %q; want AccessDenied of the start", f.Reason, f.Operation)
	}
	if e.world.Now() != fleettest.New().Now().Add(15*time.Second) {
		t.Errorf("the run waited %s; the denial ends the wait after one poll", e.world.Now().Sub(fleettest.New().Now()))
	}
}

// The answers of EKS that end the refresh at once. Each blocks the run,
// and none reads the insights, which are stale.
func TestInsightsRefresh_FailuresBlock(t *testing.T) {
	tests := []struct {
		name  string
		setup func(e *env)
		want  []string
		op    fleettest.Operation
	}{
		{
			name:  "a new cluster has no insights yet",
			setup: func(e *env) { refuseStart(e.world, codeNotFound) },
			want:  []string{codeNotFound},
			op:    fleettest.EKSStartInsightsRefresh,
		},
		{
			name:  "a denied start is one line, and the failure names the action",
			setup: func(e *env) { e.world.SetFault(fleettest.EKSStartInsightsRefresh, fleettest.Fault{Deny: true}) },
			want:  []string{"AccessDenied"},
			op:    fleettest.EKSStartInsightsRefresh,
		},
		{
			name: "a refresh that failed",
			setup: func(e *env) {
				e.onRequest(fleettest.EKSDescribeInsightsRefresh, func(int) {
					e.world.Edit(func(s *fleettest.State) {
						s.Cluster(east, "prod").InsightsRefresh.Status = fleettest.RefreshFailed
					})
				})
			},
			want: []string{"the insights refresh of cluster prod failed"},
		},
		{
			name: "a denied poll",
			setup: func(e *env) {
				e.world.SetFault(fleettest.EKSDescribeInsightsRefresh, fleettest.Fault{Deny: true})
			},
			want: []string{"AccessDenied"},
			op:   fleettest.EKSDescribeInsightsRefresh,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t, engineWorld(nil))
			tt.setup(e)
			res := mustRun(t, e, upgradeTo("1.33"), Hooks{})
			msg, f := wantRefreshBlocked(t, e, res, tt.want...)
			if strings.Contains(msg, "\n") {
				t.Errorf("the blocker is more than one line: %q", msg)
			}
			if f.Operation != string(tt.op) {
				t.Errorf("the failure names the call %q, want %q", f.Operation, tt.op)
			}
			if n := e.count(fleettest.EKSListInsights); n != 0 {
				t.Errorf("%d reads of the insights after a refresh that did not complete, want none", n)
			}
			if e.world.Now() != fleettest.New().Now() {
				t.Errorf("the run waited %s for a refresh that had ended", e.world.Now().Sub(fleettest.New().Now()))
			}
		})
	}
}

// A new cluster is told to run again later, in the words of the refresher
// ("a new cluster can take 15 minutes"), and not only the API error.
func TestInsightsRefresh_NewClusterSaysWhenToRetry(t *testing.T) {
	e := newEnv(t, engineWorld(nil))
	refuseStart(e.world, codeNotFound)
	res := mustRun(t, e, upgradeTo("1.33"), Hooks{})
	wantRefreshBlocked(t, e, res, "has not made cluster insights available for prod yet", "run again later")
}

// A refresh that failed is not run a second time by the same plan. A
// start that stays refused blocks after one wait of five minutes.
func TestInsightsRefresh_FailedRefreshRunsOnce(t *testing.T) {
	e := newEnv(t, engineWorld(nil))
	refuseStart(e.world, codeInvalidRequest)
	hideRefresh(e.world)
	res := mustRun(t, e, upgradeTo("1.33"), Hooks{})
	wantRefreshBlocked(t, e, res)
	if got := e.world.Now().Sub(fleettest.New().Now()); got != insightsRefreshLimit {
		t.Errorf("the run waited %s for the refresh, want %s", got, insightsRefreshLimit)
	}
}

// Ctrl+C or the deadline of the caller while the refresh of the plan is
// polled ends the run as interrupted or timed out. It is not a blocked
// plan: nothing says that the cluster is not ready.
func TestInsightsRefresh_ContextEndsDuringTheRefresh(t *testing.T) {
	tests := []struct {
		name   string
		cause  error
		status RunStatus
		want   string
	}{
		{"interrupt", context.Canceled, Interrupted, "interrupted before anything was changed"},
		{"deadline", context.DeadlineExceeded, Failed, "timed out reading prod"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			world := engineWorld(func(s *fleettest.State, _ *fleettest.Cluster) { s.InsightsRefresh = time.Hour })
			e := newEnv(t, world)
			ctx, cancel := context.WithCancelCause(testContext(t))
			defer cancel(nil)
			// The second poll is the handshake: the wait is under way.
			e.onRequest(fleettest.EKSDescribeInsightsRefresh, func(n int) {
				if n == 2 {
					cancel(tt.cause)
				}
			})
			res, err := Run(ctx, e.deps, upgradeTo("1.33"), Hooks{})
			if err != nil {
				t.Fatal(err)
			}
			if res.Status != tt.status || res.ExitCode() != exitcode.Failure || res.Plan != nil {
				t.Errorf("status = %s, exit %d, plan %v; want %s, exit 1, and no plan", res.Status, res.ExitCode(), res.Plan, tt.status)
			}
			if res.Err == nil || !strings.Contains(res.Err.Error(), tt.want) {
				t.Errorf("err = %v, want %q", res.Err, tt.want)
			}
			if diff := cmp.Diff([]string{sentRefresh}, e.sent()); diff != "" {
				t.Errorf("requests (-want +got):\n%s", diff)
			}
		})
	}
}

// A completed refresh counts only when it is the one that this run
// started, or a later one. EKS refuses the first start, the describe
// shows an older refresh that completed, and the start sent again is
// accepted. While the describe still shows the older completion, the run
// waits for its own refresh.
func TestInsightsRefresh_StaleCompletionIsNotCurrent(t *testing.T) {
	// staleWorld has a refresh that completed an hour ago. The first start
	// of the run is refused. From the second describe on, the describe
	// shows that old refresh while lag lasts: lag < 0 is for ever.
	staleWorld := func(t *testing.T, lag int) *env {
		t.Helper()
		world := engineWorld(nil)
		if err := world.StartInsightsRefresh(east, "prod"); err != nil {
			t.Fatal(err)
		}
		world.Advance(time.Hour)
		var old fleettest.InsightsRefresh
		world.View(func(s *fleettest.State) { old = s.Cluster(east, "prod").InsightsRefresh })
		e := newEnv(t, world)
		refuseStart(world, codeInvalidRequest)
		e.onRequest(fleettest.EKSDescribeInsightsRefresh, func(n int) {
			switch {
			case n == 1:
				world.SetFault(fleettest.EKSStartInsightsRefresh, fleettest.Fault{})
			case lag < 0 || n <= 1+lag:
				world.Edit(func(s *fleettest.State) { s.Cluster(east, "prod").InsightsRefresh = old })
			case n == 2+lag:
				// The refresh of the run shows up at last.
				if err := world.StartInsightsRefresh(east, "prod"); err != nil {
					t.Error(err)
				}
			}
		})
		return e
	}
	t.Run("the describe never shows the refresh of the run", func(t *testing.T) {
		e := staleWorld(t, -1)
		var lines []string
		res := mustRun(t, e, upgradeTo("1.33"), progressOf(&lines))
		wantRefreshBlocked(t, e, res, "did not finish in time")
		if !slices.ContainsFunc(lines, func(l string) bool { return strings.Contains(l, "earlier insights refresh") }) {
			t.Errorf("no progress line says that the describe shows an older refresh: %q", lines)
		}
	})
	t.Run("the describe shows the refresh of the run later", func(t *testing.T) {
		e := staleWorld(t, 2)
		res := mustRun(t, e, upgradeTo("1.33"), Hooks{})
		if res.Status != Succeeded || res.ExitCode() != exitcode.OK {
			t.Fatalf("status = %s, exit %d, err %v; want Succeeded", res.Status, res.ExitCode(), res.Err)
		}
		if n := e.count(fleettest.EKSDescribeInsightsRefresh); n < 4 {
			t.Errorf("%d describes; the run took a stale completion before its own refresh showed up", n)
		}
	})
}

// The run tells its own refresh from the earlier one by their start
// times, both on the clock of EKS. A clock of the run that is an hour
// ahead of EKS changes nothing.
func TestInsightsRefresh_ClockOfTheRunDoesNotMatter(t *testing.T) {
	world := engineWorld(nil)
	if err := world.StartInsightsRefresh(east, "prod"); err != nil {
		t.Fatal(err)
	}
	world.Advance(time.Hour)
	e := newEnv(t, world)
	e.deps.Now = func() time.Time { return world.Now().Add(time.Hour) }
	refuseStart(world, codeInvalidRequest)
	e.onRequest(fleettest.EKSDescribeInsightsRefresh, func(n int) {
		if n == 1 {
			world.SetFault(fleettest.EKSStartInsightsRefresh, fleettest.Fault{})
		}
	})
	if err := refreshOnce(testContext(t), e); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if n := e.count(fleettest.EKSStartInsightsRefresh); n != 2 {
		t.Errorf("%d starts, want 2: the refused one and the one that EKS accepted", n)
	}
}
