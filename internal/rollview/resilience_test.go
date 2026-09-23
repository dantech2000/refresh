package rollview

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/dantech2000/refresh/internal/noderoll"
	"github.com/dantech2000/refresh/internal/render"
	"github.com/dantech2000/refresh/internal/ui"
)

// flakyObserver serves frames from a ScriptedObserver but fails the reads
// whose 1-based call index is in failOn.
type flakyObserver struct {
	inner  *noderoll.ScriptedObserver
	failOn map[int]bool
	calls  int
}

func (o *flakyObserver) Snapshot(ctx context.Context) (noderoll.Snapshot, error) {
	o.calls++
	if o.failOn[o.calls] {
		return noderoll.Snapshot{}, errors.New("the server is currently unable to handle the request")
	}
	return o.inner.Snapshot(ctx)
}

// frameRecorder keeps each Write separately. In append mode the live region
// writes a whole frame per call, so writes are frames.
type frameRecorder struct {
	bytes.Buffer
	writes []string
}

func (r *frameRecorder) Write(p []byte) (int, error) {
	r.writes = append(r.writes, string(p))
	return r.Buffer.Write(p)
}

// Two failed reads mid-roll must not end the panel: the last good frame stays
// up with a one-line retry notice, and the roll then runs to completion once
// reads recover.
func TestRunRoll_RecoversFromTransientObserverErrors(t *testing.T) {
	var buf frameRecorder
	th := render.New(render.ColorNone, true)
	obs := &flakyObserver{
		inner:  noderoll.NewScriptedObserver(noderoll.DemoTimeline()),
		failOn: map[int]bool{3: true, 4: true},
	}
	m := rollMeta{Nodegroup: "spot-burst", OldAMI: "ami-old", NewAMI: "ami-new", Desired: 3}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := runRoll(ctx, th, &buf, obs, m, time.Millisecond, rollComplete(m.Desired)); err != nil {
		t.Fatalf("runRoll = %v, want nil (roll completes after recovery)", err)
	}
	out := buf.String()

	for _, want := range []string{"retrying, 1/", "retrying, 2/"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing retry notice %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "giving up") {
		t.Errorf("two transient failures must not end the panel:\n%s", out)
	}
	// The error frames keep the last good panel above the notice.
	for _, frame := range buf.writes {
		if strings.Contains(frame, "retrying") && !strings.Contains(frame, "rolling spot-burst") {
			t.Errorf("error frame dropped the last good panel:\n%s", frame)
		}
	}
	if !obs.inner.AtEnd() {
		t.Error("panel stopped before the scripted roll reached its final frame")
	}
}

// Only maxConsecutiveObserverErrors failures in a row end the panel.
func TestRunRoll_GivesUpAfterConsecutiveErrors(t *testing.T) {
	var buf bytes.Buffer
	th := render.New(render.ColorNone, true)
	failOn := map[int]bool{}
	for i := 2; i < 2+maxConsecutiveObserverErrors; i++ {
		failOn[i] = true
	}
	obs := &flakyObserver{inner: noderoll.NewScriptedObserver(noderoll.DemoTimeline()), failOn: failOn}
	m := rollMeta{Nodegroup: "spot-burst", Desired: 3}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := runRoll(ctx, th, &buf, obs, m, time.Millisecond, rollComplete(m.Desired)); err != nil {
		t.Fatalf("runRoll = %v", err)
	}
	if obs.calls != 1+maxConsecutiveObserverErrors {
		t.Errorf("snapshot reads = %d, want %d (stop at the limit)", obs.calls, 1+maxConsecutiveObserverErrors)
	}
	if !strings.Contains(buf.String(), "giving up") {
		t.Errorf("output missing give-up notice:\n%s", buf.String())
	}
}

func TestOneLineWarn(t *testing.T) {
	// Collapses whitespace.
	if got := oneLineWarn("  evict\n\tfailed   now "); got != "evict failed now" {
		t.Errorf("oneLineWarn = %q", got)
	}
	// Multi-byte runes near the cut stay whole, and the result fits the cap.
	long := strings.Repeat("é", warnMsgMaxWidth+10)
	got := oneLineWarn(long)
	if !utf8.ValidString(got) {
		t.Errorf("truncation split a UTF-8 sequence: %q", got)
	}
	if w := ui.VisibleWidth(got); w > warnMsgMaxWidth {
		t.Errorf("width = %d, want <= %d", w, warnMsgMaxWidth)
	}
	// Wide (2-cell) runes are measured in cells, not bytes or runes.
	wide := oneLineWarn(strings.Repeat("日", warnMsgMaxWidth))
	if w := ui.VisibleWidth(wide); w > warnMsgMaxWidth {
		t.Errorf("wide-rune width = %d, want <= %d", w, warnMsgMaxWidth)
	}
	// Short messages pass through untouched.
	if got := oneLineWarn("short"); got != "short" {
		t.Errorf("oneLineWarn(short) = %q", got)
	}
}
