package changecmd

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/dantech2000/refresh/internal/change"
	"github.com/dantech2000/refresh/internal/cmdutil"
	"github.com/dantech2000/refresh/internal/noderoll"
)

// piped returns streams whose stderr is not a terminal, and the buffer
// that it writes to.
func piped() (*cmdutil.IOStreams, *bytes.Buffer) {
	var errOut bytes.Buffer
	return &cmdutil.IOStreams{Out: &bytes.Buffer{}, ErrOut: &errOut, NoColor: true}, &errOut
}

// draining returns the NodeProgress event of nodes with web-1 draining.
func draining(ref change.StepRef, at time.Time) change.Event {
	e := nodes(ref, at)
	e.Nodes.Snapshot.Draining = 1
	e.Nodes.Snapshot.Nodes[0].Phase = noderoll.PhaseDraining
	return e
}

// Without a terminal the panel cannot be redrawn, so its frames are
// appended. A frame equal to the last one is not appended again, and a
// frame that differs waits for the frame interval: a roll of 40 minutes
// does not flood a log.
func TestRollRenderer_AppendsAFrameOnlyWhenItChangedAndIsDue(t *testing.T) {
	ios, errOut := piped()
	r := NewRollRenderer(ios)
	ref := rollRef("prod", "web")
	at := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	r.Render(change.Event{Kind: change.PlanReady, Plan: rollPlan(map[string]string{"prod": "ami-new"})})
	errOut.Reset()

	frames := func() int { return strings.Count(errOut.String(), "web-1") }
	r.Render(nodes(ref, at))
	if frames() != 1 {
		t.Fatalf("the first snapshot wrote %d frame(s), want 1:\n%s", frames(), errOut)
	}
	if strings.Contains(errOut.String(), "\x1b[") {
		t.Errorf("a frame without a terminal has cursor codes:\n%q", errOut)
	}
	// A minute of snapshots that show the same nodes.
	for i := 1; i <= 60; i++ {
		r.Render(nodes(ref, at.Add(time.Duration(i)*time.Second)))
	}
	if frames() != 1 {
		t.Errorf("%d frames after a minute with no change, want the first one alone", frames())
	}
	// A change shows when the interval has passed since the last frame,
	// and not before.
	r.Render(draining(ref, at.Add(frameInterval-time.Second)))
	if frames() != 1 {
		t.Errorf("%d frames: a changed frame was appended before the interval passed", frames())
	}
	r.Render(draining(ref, at.Add(frameInterval)))
	if frames() != 2 {
		t.Errorf("%d frames after a change and the interval, want 2", frames())
	}
	// Changes that come faster than the interval are one frame each
	// interval.
	for i := 1; i < 15; i++ {
		e := nodes(ref, at.Add(frameInterval+time.Duration(i)*time.Second))
		if i%2 == 1 {
			e = draining(ref, e.At)
		} else {
			e.Nodes.Snapshot.Joining = i
		}
		r.Render(e)
	}
	if frames() != 2 {
		t.Errorf("%d frames: frames were appended faster than one each %s", frames(), frameInterval)
	}
}

// A panel that never comes up, because no snapshot arrives, must not
// leave the wait silent: the lines of the roll print as they come, with
// and without a terminal, and none is held for a panel.
func TestRollRenderer_NoPanelLeavesTheLinesOfTheWait(t *testing.T) {
	for name, streams := range map[string]func() (*cmdutil.IOStreams, *bytes.Buffer){"a terminal": terminal, "no terminal": piped} {
		t.Run(name, func(t *testing.T) {
			ios, errOut := streams()
			r := NewRollRenderer(ios)
			ref := rollRef("prod", "web")
			at := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
			r.Render(change.Event{Kind: change.PlanReady, Plan: rollPlan(map[string]string{"prod": "ami-new"})})
			errOut.Reset()

			said := func(what string, e change.Event, want string) {
				t.Helper()
				before := errOut.Len()
				r.Render(e)
				if got := errOut.String()[before:]; !strings.Contains(got, want) {
					t.Errorf("%s wrote %q at once, want a line with %q", what, got, want)
				}
			}
			said("the notice of the observer that failed", change.Event{Kind: change.Notice, At: at,
				Text: "could not read the nodes of nodegroup web (forbidden); the roll goes on without the node view"}, "the roll goes on without the node view")
			started := change.StepResult{Ref: ref, Status: change.StepStarted, UpdateID: "u-1", StartedAt: at}
			said("the start of the roll", change.Event{Kind: change.StepChanged, At: at, Phase: ref.Phase, Step: ref, Result: &started}, "web")
			said("a poll of the update", change.Event{Kind: change.UpdatePolled, At: at.Add(15 * time.Second), Phase: ref.Phase, Step: ref,
				Update: &change.UpdateProgress{Resource: ref.Resource(), UpdateID: "u-1", Status: "InProgress"}}, "InProgress")
			done := started
			done.Status, done.FinishedAt = change.StepSucceeded, at.Add(time.Minute)
			said("the end of the roll", change.Event{Kind: change.StepChanged, At: done.FinishedAt, Phase: ref.Phase, Step: ref, Result: &done}, "Succeeded")
			if strings.Contains(errOut.String(), "\x1b[") {
				t.Errorf("cursor codes with no panel on the screen:\n%q", errOut)
			}
		})
	}
}
