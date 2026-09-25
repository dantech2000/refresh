package monitoring

import (
	"bytes"
	"errors"
	"io"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"

	"github.com/dantech2000/refresh/internal/render"
	refreshTypes "github.com/dantech2000/refresh/internal/types"
)

// captureStdout redirects os.Stdout for fn and returns what was written.
func captureStdout(fn func()) string {
	r, w, _ := os.Pipe()
	old := os.Stdout
	os.Stdout = w
	fn()
	_ = w.Close()
	os.Stdout = old
	var buf bytes.Buffer
	_, _ = io.Copy(&buf, r)
	return buf.String()
}

func singleUpdate(status ekstypes.UpdateStatus) refreshTypes.UpdateProgress {
	return refreshTypes.UpdateProgress{
		NodegroupName: "ng-1",
		ClusterName:   "my-cluster",
		UpdateID:      "upd-abc",
		Status:        status,
		StartTime:     time.Now().Add(-10 * time.Second),
		LastChecked:   time.Now(),
	}
}

// plainTheme is a Unicode theme without color, so lines compare as text.
func plainTheme() *render.Theme { return render.New(render.ColorNone, true) }

// frameRecorder is a FrameDrawer that keeps every frame it is given.
type frameRecorder struct{ frames [][]string }

func (f *frameRecorder) Draw(frame []string) { f.frames = append(f.frames, frame) }

// recordFrames makes the display draw into a recorder with a plain theme.
func recordFrames(t *testing.T) *frameRecorder {
	t.Helper()
	rec := &frameRecorder{}
	oldDrawer, oldTheme := newFrameDrawer, displayTheme
	newFrameDrawer = func() refreshTypes.FrameDrawer { return rec }
	displayTheme = plainTheme
	t.Cleanup(func() { newFrameDrawer, displayTheme = oldDrawer, oldTheme })
	return rec
}

func joined(lines []string) string { return strings.Join(lines, "\n") }

// ──────────────────────────────────────────────────────────────────────────────
// progress tree
// ──────────────────────────────────────────────────────────────────────────────

func TestUpdateProgressTree_Empty(t *testing.T) {
	if got := updateProgressTree(plainTheme(), nil); len(got) != 0 {
		t.Errorf("empty updates rendered %q", got)
	}
}

func TestUpdateProgressTree_SingleUpdateToken(t *testing.T) {
	out := joined(updateProgressTree(plainTheme(), []refreshTypes.UpdateProgress{singleUpdate(ekstypes.UpdateStatusInProgress)}))
	for _, want := range []string{"└── ◷ IN PROGRESS ng-1", "├── Status: ◷ InProgress", "└── Last Checked: "} {
		if !strings.Contains(out, want) {
			t.Errorf("progress tree missing %q:\n%s", want, out)
		}
	}
}

func TestUpdateProgressTree_MultipleUpdates_LastHasCornerPrefix(t *testing.T) {
	updates := []refreshTypes.UpdateProgress{
		singleUpdate(ekstypes.UpdateStatusInProgress),
		singleUpdate(ekstypes.UpdateStatusSuccessful),
	}
	updates[1].NodegroupName = "ng-2"
	out := joined(updateProgressTree(plainTheme(), updates))
	if !strings.Contains(out, "├── ◷ IN PROGRESS ng-1") || !strings.Contains(out, "└── ● SUCCESSFUL ng-2") {
		t.Errorf("tree branches or tokens wrong:\n%s", out)
	}
}

func TestUpdateProgressTree_ErrorMessageShown(t *testing.T) {
	update := singleUpdate(ekstypes.UpdateStatusFailed)
	update.ErrorMessage = "disk full"
	out := joined(updateProgressTree(plainTheme(), []refreshTypes.UpdateProgress{update}))
	if !strings.Contains(out, "Status: ✗ Failed: disk full") {
		t.Errorf("error message should appear with a fail token, got:\n%s", out)
	}
}

// An update whose status could not be polled has an unknown outcome: the
// unknown token, never the FAILED one.
func TestUpdateProgressTree_MonitorErrIsUnknown(t *testing.T) {
	update := singleUpdate(ekstypes.UpdateStatusInProgress)
	update.MonitorErr = errors.New("AccessDenied: no\nsecond line")
	out := joined(updateProgressTree(plainTheme(), []refreshTypes.UpdateProgress{update}))
	if !strings.Contains(out, "○ MONITORING FAILED ng-1") || !strings.Contains(out, "Status: ○ MONITORING FAILED: AccessDenied: no") {
		t.Errorf("unmonitored update rendering wrong:\n%s", out)
	}
	if strings.Contains(out, "✗") || strings.Contains(out, "second line") {
		t.Errorf("unmonitored update must not read as failed or print the whole error:\n%s", out)
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// completion tree
// ──────────────────────────────────────────────────────────────────────────────

func TestCompletionSummaryTree_Tokens(t *testing.T) {
	cases := []struct {
		status ekstypes.UpdateStatus
		want   string
	}{
		{ekstypes.UpdateStatusSuccessful, "Status: ● SUCCESSFUL"},
		{ekstypes.UpdateStatusFailed, "Status: ✗ FAILED"},
		{ekstypes.UpdateStatusCancelled, "Status: ▲ CANCELLED"},
		{"SOME_UNKNOWN_STATUS", "Status: ○ SOME_UNKNOWN_STATUS"},
	}
	for _, c := range cases {
		out := joined(completionSummaryTree(plainTheme(), []refreshTypes.UpdateProgress{singleUpdate(c.status)}))
		if !strings.Contains(out, c.want) {
			t.Errorf("%s: completion tree missing %q:\n%s", c.status, c.want, out)
		}
	}
	if got := completionSummaryTree(plainTheme(), nil); len(got) != 0 {
		t.Errorf("empty updates rendered %q", got)
	}
}

func TestCompletionSummaryTree_FailedWithMessage(t *testing.T) {
	update := singleUpdate(ekstypes.UpdateStatusFailed)
	update.ErrorMessage = "timeout error"
	out := joined(completionSummaryTree(plainTheme(), []refreshTypes.UpdateProgress{update}))
	if !strings.Contains(out, "✗ FAILED: timeout error") {
		t.Errorf("error message should appear in completion tree, got:\n%s", out)
	}
}

// Without Unicode the trees still carry the status in ASCII, and without
// color no escape codes are written: color is additive.
func TestTrees_ASCIIFallback(t *testing.T) {
	th := render.New(render.ColorNone, false)
	updates := []refreshTypes.UpdateProgress{singleUpdate(ekstypes.UpdateStatusInProgress), singleUpdate(ekstypes.UpdateStatusFailed)}
	out := joined(updateProgressTree(th, updates)) + joined(completionSummaryTree(th, updates))
	for _, want := range []string{"[~] IN PROGRESS ng-1", "[X] FAILED"} {
		if !strings.Contains(out, want) {
			t.Errorf("ASCII tree missing %q:\n%s", want, out)
		}
	}
	if strings.ContainsAny(out, "●◷✗▲○\x1b") {
		t.Errorf("ASCII tree has Unicode status glyphs or ANSI:\n%s", out)
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// DisplayProgressUpdate
// ──────────────────────────────────────────────────────────────────────────────

// Each poll draws one frame through the live region, which repaints it in
// place on a terminal (render.LiveRegion counts wrapped rows) and appends
// it when piped.
func TestDisplayProgressUpdate_DrawsThroughLiveRegion(t *testing.T) {
	rec := recordFrames(t)
	monitor := &refreshTypes.ProgressMonitor{StartTime: time.Now()}
	monitor.Updates = append(monitor.Updates, singleUpdate(ekstypes.UpdateStatusInProgress))

	DisplayProgressUpdate(monitor)
	DisplayProgressUpdate(monitor)
	if len(rec.frames) != 2 {
		t.Fatalf("frames drawn = %d, want 2", len(rec.frames))
	}
	if monitor.Live != refreshTypes.FrameDrawer(rec) {
		t.Error("the monitor should keep its live region across polls")
	}
	frame := joined(rec.frames[0])
	if !strings.HasPrefix(frame, "Elapsed: ") || !strings.Contains(frame, "\nmy-cluster\n") || !strings.Contains(frame, "ng-1") {
		t.Errorf("frame = %q", frame)
	}
}

func TestProgressLines_EmptyMonitor(t *testing.T) {
	monitor := &refreshTypes.ProgressMonitor{StartTime: time.Now()}
	got := joined(progressLines(plainTheme(), monitor))
	// Only the elapsed line and the trailing blank line: no cluster root and
	// no tree.
	if !regexp.MustCompile(`^Elapsed: \S+\n$`).MatchString(got) {
		t.Errorf("empty monitor frame = %q, want just the elapsed line", got)
	}
}

// Piped output: the real live region appends frames and never writes cursor
// controls.
func TestDisplayProgressUpdate_NonInteractiveAppendsOnly(t *testing.T) {
	monitor := &refreshTypes.ProgressMonitor{StartTime: time.Now()}
	monitor.Updates = append(monitor.Updates, singleUpdate(ekstypes.UpdateStatusInProgress))
	out := captureStdout(func() {
		DisplayProgressUpdate(monitor)
		monitor.Updates[0].Status = ekstypes.UpdateStatusSuccessful
		DisplayProgressUpdate(monitor)
	})
	if strings.Contains(out, "\033[") {
		t.Errorf("piped output must not contain cursor-control codes, got %q", out)
	}
	if strings.Count(out, "Elapsed: ") != 2 {
		t.Errorf("piped output should append both frames, got %q", out)
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// DisplayCompletionSummary — verbose path
// ──────────────────────────────────────────────────────────────────────────────

func TestDisplayCompletionSummary_VerboseOutputsResults(t *testing.T) {
	monitor := &refreshTypes.ProgressMonitor{StartTime: time.Now().Add(-5 * time.Second)}
	monitor.Updates = append(monitor.Updates, singleUpdate(ekstypes.UpdateStatusSuccessful))
	out := captureStdout(func() {
		_ = DisplayCompletionSummary(monitor, refreshTypes.MonitorConfig{})
	})
	if !strings.Contains(out, "Results: 1 successful, 0 failed") {
		t.Errorf("verbose mode should show results summary, got:\n%s", out)
	}
}

// After progress frames, the summary replaces the last frame through the same
// live region instead of printing under it.
func TestDisplayCompletionSummary_ReplacesProgressFrame(t *testing.T) {
	rec := recordFrames(t)
	monitor := &refreshTypes.ProgressMonitor{StartTime: time.Now()}
	monitor.Updates = append(monitor.Updates, singleUpdate(ekstypes.UpdateStatusSuccessful))
	DisplayProgressUpdate(monitor)
	out := captureStdout(func() {
		_ = DisplayCompletionSummary(monitor, refreshTypes.MonitorConfig{})
	})
	if out != "" {
		t.Errorf("summary printed around the live region: %q", out)
	}
	if len(rec.frames) != 2 || !strings.Contains(joined(rec.frames[1]), "All updates completed") {
		t.Fatalf("summary frame not drawn in place: %q", rec.frames)
	}
}

func TestDisplayCompletionSummary_VerboseEmptyUpdates(t *testing.T) {
	monitor := &refreshTypes.ProgressMonitor{StartTime: time.Now()}
	var err error
	out := stripANSI(captureStdout(func() {
		err = DisplayCompletionSummary(monitor, refreshTypes.MonitorConfig{})
	}))
	if err != nil {
		t.Errorf("no updates: err = %v, want nil", err)
	}
	if !strings.Contains(out, "All updates completed") || !strings.Contains(out, "Results: 0 successful, 0 failed") {
		t.Errorf("summary = %q, want completion line and zero results", out)
	}
	if strings.Contains(out, "── ") || strings.Contains(out, "not monitored") {
		t.Errorf("no updates must print no tree and no unmonitored count, got %q", out)
	}
}

var ansiEscape = regexp.MustCompile(`\x1b\[[0-9;]*[A-Za-z]`)

// stripANSI removes color and cursor codes so assertions read the text.
func stripANSI(s string) string { return ansiEscape.ReplaceAllString(s, "") }

// ──────────────────────────────────────────────────────────────────────────────
// printMonitoringHeader
// ──────────────────────────────────────────────────────────────────────────────

func TestPrintMonitoringHeader_ContainsUpdateCount(t *testing.T) {
	monitor := &refreshTypes.ProgressMonitor{}
	monitor.Updates = append(monitor.Updates, singleUpdate(ekstypes.UpdateStatusInProgress))
	monitor.Updates = append(monitor.Updates, singleUpdate(ekstypes.UpdateStatusInProgress))
	cfg := refreshTypes.MonitorConfig{
		Quiet:        false,
		PollInterval: 5 * time.Second,
		Timeout:      10 * time.Minute,
	}
	out := captureStdout(func() {
		printMonitoringHeader(monitor, cfg)
	})
	if !strings.Contains(out, "2") {
		t.Errorf("header should mention update count, got:\n%s", out)
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// handleUserCancellation — verbose path
// ──────────────────────────────────────────────────────────────────────────────

func TestHandleUserCancellation_VerboseWithUpdates(t *testing.T) {
	monitor := &refreshTypes.ProgressMonitor{}
	monitor.Updates = append(monitor.Updates, singleUpdate(ekstypes.UpdateStatusInProgress))
	cfg := refreshTypes.MonitorConfig{Quiet: false}
	var err error
	out := captureStdout(func() { err = handleUserCancellation(monitor, cfg) })
	if !errors.Is(err, ErrCancelled) {
		t.Errorf("expected ErrCancelled, got %v", err)
	}
	// The status-check hint comes from the caller's returned error (updateExit);
	// printing it here too would show it twice.
	if strings.Contains(out, "refresh nodegroup list") || strings.Contains(out, "refresh list") {
		t.Errorf("cancellation display must not repeat the status-check hint, got:\n%s", out)
	}
}
