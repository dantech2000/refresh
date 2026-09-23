package nodegroup

import (
	"context"
	"errors"
	"testing"
	"time"
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
			return nil
		})
	if err != nil || !heldBack || calls != 1 {
		t.Fatalf("err = %v, heldBack = %v, calls = %d; want nil, true, 1", err, heldBack, calls)
	}
}
