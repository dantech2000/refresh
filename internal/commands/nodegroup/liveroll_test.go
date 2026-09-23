package nodegroup

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/dantech2000/refresh/internal/monitoring"
)

// A panel that returns at once (kube context mismatch, no labelled nodes,
// baseline failure) must not leave the EKS wait silent: the quiet monitor is
// stopped and a loud one takes over, and its result is what comes back.
func TestMonitorAlongsidePanel_PanelReturnsEarlyResumesLoud(t *testing.T) {
	wantErr := errors.New("one or more nodegroup updates failed: ng: PodEvictionFailure")
	var calls []bool
	heldBack, err := monitorAlongsidePanel(context.Background(), func(context.Context) {}, time.Hour,
		func(ctx context.Context, quiet bool) error {
			calls = append(calls, quiet)
			if quiet {
				<-ctx.Done() // would poll until the update ends
				return nil
			}
			if _, ok := ctx.Deadline(); !ok {
				t.Error("loud run lost the original deadline")
			}
			return wantErr
		})

	if !errors.Is(err, wantErr) {
		t.Fatalf("err = %v, want %v", err, wantErr)
	}
	if heldBack {
		t.Fatal("heldBack = true; the loud run already printed its own output")
	}
	if len(calls) != 2 || !calls[0] || calls[1] {
		t.Fatalf("monitor quiet flags = %v, want [true false]", calls)
	}
}

// While the panel draws, the monitor stays quiet for the whole wait, and its
// result is returned with heldBack so the caller prints the summary.
func TestMonitorAlongsidePanel_PanelDrawsUntilTerminal(t *testing.T) {
	wantErr := errors.New("update failed")
	panelStopped := false
	var calls []bool
	heldBack, err := monitorAlongsidePanel(context.Background(), func(ctx context.Context) {
		<-ctx.Done()
		panelStopped = true
	}, time.Hour, func(_ context.Context, quiet bool) error {
		calls = append(calls, quiet)
		time.Sleep(20 * time.Millisecond)
		return wantErr
	})

	if !errors.Is(err, wantErr) || !heldBack {
		t.Fatalf("err = %v, heldBack = %v; want %v, true", err, heldBack, wantErr)
	}
	if !panelStopped {
		t.Fatal("panel was not cancelled and joined")
	}
	if len(calls) != 1 || !calls[0] {
		t.Fatalf("monitor quiet flags = %v, want [true]", calls)
	}
}

// --timeout 0 means no monitor limit: the restarted monitor must not get a
// deadline, and a user cancel during it must come back as ErrCancelled (so the
// caller skips post-roll verification).
func TestMonitorAlongsidePanel_ZeroTimeoutAndCancelOnRestart(t *testing.T) {
	heldBack, err := monitorAlongsidePanel(context.Background(), func(context.Context) {}, 0,
		func(ctx context.Context, quiet bool) error {
			if quiet {
				<-ctx.Done()
				return monitoring.ErrCancelled
			}
			if _, ok := ctx.Deadline(); ok {
				t.Error("restarted monitor got a deadline with --timeout 0")
			}
			return monitoring.ErrCancelled // user pressed Ctrl+C during the loud run
		})
	if heldBack || !errors.Is(err, monitoring.ErrCancelled) {
		t.Fatalf("heldBack = %v, err = %v; want false, ErrCancelled", heldBack, err)
	}
	if shouldVerifyPostRoll(context.Background(), err) {
		t.Fatal("verification must be skipped after a cancelled restarted monitor")
	}
}

// A user cancel (parent ctx) ends the quiet run; it must not be mistaken for
// the panel stopping early and restart the monitor.
func TestMonitorAlongsidePanel_ParentCancelDoesNotResume(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	heldBack, err := monitorAlongsidePanel(ctx, func(pctx context.Context) { <-pctx.Done() }, time.Hour,
		func(mctx context.Context, _ bool) error {
			calls++
			cancel()
			<-mctx.Done()
			return monitoring.ErrCancelled
		})
	if !errors.Is(err, monitoring.ErrCancelled) || !heldBack || calls != 1 {
		t.Fatalf("err = %v, heldBack = %v, calls = %d; want ErrCancelled, true, 1", err, heldBack, calls)
	}
}

// The live panel is the default only on an interactive stdout (a color
// terminal); piped/CI/NO_COLOR runs keep the monitor's progress lines unless
// --live forces the panel. Multi-nodegroup and quiet/JSON runs never get it.
func TestShowLivePanel(t *testing.T) {
	cases := []struct {
		name                     string
		updates                  int
		quiet, live, interactive bool
		want                     bool
	}{
		{"interactive single roll", 1, false, false, true, true},
		{"piped or NO_COLOR", 1, false, false, false, false},
		{"--live overrides a pipe", 1, false, true, false, true},
		{"--live on a terminal", 1, false, true, true, true},
		{"quiet wins over --live", 1, true, true, true, false},
		{"several nodegroups", 2, false, true, true, false},
		{"nothing started", 0, false, true, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := showLivePanel(tc.updates, tc.quiet, tc.live, tc.interactive); got != tc.want {
				t.Errorf("showLivePanel = %v, want %v", got, tc.want)
			}
		})
	}
}
