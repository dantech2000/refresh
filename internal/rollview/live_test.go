package rollview

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/fatih/color"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/dantech2000/refresh/internal/services/common"
)

// captureStdout runs fn and returns everything written to os.Stdout and to
// fatih/color's writer while it ran. The color library snapshots os.Stdout at
// package init, so we override color.Output too.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	originalStdout := os.Stdout
	originalColorOutput := color.Output
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	color.Output = w
	t.Cleanup(func() {
		os.Stdout = originalStdout
		color.Output = originalColorOutput
	})
	fn()
	_ = w.Close()
	var buf bytes.Buffer
	_, _ = io.Copy(&buf, r)
	return buf.String()
}

// LiveRollForUpdate must be best-effort and bounded: a nil client returns
// immediately, and a cluster that never converges is bounded by the timeout
// (never hangs the update). Output is swallowed via captureStdout.
func TestLiveRollForUpdate_DegradesAndBounds(t *testing.T) {
	_ = captureStdout(t, func() {
		// nil client → immediate, no panic.
		LiveRollForUpdate(context.Background(), nil, "ng", time.Second, time.Second)

		// A fake cluster whose old node never gets replaced → bounded by timeout.
		client := fake.NewClientset(kn("ip-1", true, false))
		start := time.Now()
		LiveRollForUpdate(context.Background(), client, "spot-burst", 40*time.Millisecond, 10*time.Millisecond)
		if elapsed := time.Since(start); elapsed > 5*time.Second {
			t.Fatalf("LiveRollForUpdate did not respect timeout bound: %v", elapsed)
		}
	})
}

// A failed roll (e.g. PodEvictionFailure) never converges, so the panel alone
// would run until its hour-long timeout. Run alongside the authoritative wait,
// it must stop as soon as that wait reports the failure, and the failure must
// be what the caller gets back.
func TestLiveRollForUpdate_StopsWhenUpdateFails(t *testing.T) {
	client := fake.NewClientset(kn("ip-1", true, false), kn("ip-2", true, true))
	wantErr := errors.New("nodegroup spot-burst failed: PodEvictionFailure")

	var err error
	var elapsed time.Duration
	out := captureStdout(t, func() {
		start := time.Now()
		err = common.RunAlongside(context.Background(), func(ctx context.Context) {
			LiveRollForUpdate(ctx, client, "spot-burst", time.Hour, 10*time.Millisecond)
		}, func(context.Context) error {
			time.Sleep(100 * time.Millisecond)
			return wantErr
		})
		elapsed = time.Since(start)
	})

	if !errors.Is(err, wantErr) {
		t.Fatalf("err = %v, want %v", err, wantErr)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("panel kept running %v after the update failed", elapsed)
	}
	if !strings.Contains(out, "rolling spot-burst") {
		t.Errorf("panel never rendered; output:\n%s", out)
	}
	if strings.Contains(out, "observer error") {
		t.Errorf("cancelling the panel painted an observer error:\n%s", out)
	}
}
