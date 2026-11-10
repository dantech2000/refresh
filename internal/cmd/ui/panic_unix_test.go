//go:build unix

// On Windows, detachStderr reassigns os.Stderr, which Bubble Tea reads on
// the goroutine that prints a panic: the race detector would report the
// test, not the code under it.

package ui

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/dantech2000/refresh/internal/change"
	"github.com/dantech2000/refresh/internal/tui"
	"github.com/dantech2000/refresh/internal/tui/state"
	"github.com/dantech2000/refresh/internal/view"
)

// brokenBackend panics on the first call of the model: the wait for the
// first state, which runs in a command on a goroutine of Bubble Tea.
type brokenBackend struct{}

func (brokenBackend) Next(context.Context, uint64) (state.State, error) {
	panic("the backend broke")
}
func (brokenBackend) Refresh()                                   {}
func (brokenBackend) Readiness(context.Context, state.Key) error { return nil }
func (brokenBackend) Plan(context.Context, state.Action) (state.Proposal, error) {
	return state.Proposal{}, nil
}
func (brokenBackend) Start(context.Context, state.ProposalID) (state.ChangeID, error) { return 0, nil }
func (brokenBackend) Answer(context.Context, state.ChangeID, change.QuestionID, bool) error {
	return nil
}
func (brokenBackend) Pause(context.Context, state.ChangeID) error  { return nil }
func (brokenBackend) Resume(context.Context, state.ChangeID) error { return nil }
func (brokenBackend) Stop(context.Context, state.ChangeID) error   { return nil }

// Found in review: Bubble Tea prints a panic it recovers, and its stack,
// to os.Stderr, which is the null device while the UI runs, so the user
// got "program experienced a panic" and nothing to find it by. For a
// panic in a command, Run returns before the goroutine prints at all,
// and its trace landed after the restore, a second time. The model
// recovers a panic of a command itself and quits: Bubble Tea recovers
// nothing and prints nothing, and run writes the trace once.
func TestAPanicInTheUIKeepsItsTrace(t *testing.T) {
	restore, err := detachStderr()
	if err != nil {
		t.Fatal(err)
	}
	defer restore()
	m := tui.New(t.Context(), brokenBackend{}, view.Theme{})
	var in, out bytes.Buffer
	_, err = tea.NewProgram(m, tea.WithInput(&in), tea.WithOutput(&out), tea.WithoutSignalHandler()).Run()
	restore()
	if err != nil {
		t.Fatalf("Run = %v; Bubble Tea recovered the panic, and prints it", err)
	}
	err = panicked(err, m.Panic())
	if !errors.Is(err, tea.ErrProgramPanic) {
		t.Fatalf("the session ended with %v, want ErrProgramPanic", err)
	}
	var errOut bytes.Buffer
	sayPanic(&errOut, err, m.Panic())
	for _, want := range []string{"stopped on a panic", "the backend broke", "goroutine", "panic_unix_test.go"} {
		if !strings.Contains(errOut.String(), want) {
			t.Fatalf("stderr lacks %q:\n%s", want, errOut.String())
		}
	}
}

// A panic outside the model says how to get its trace; an end without a
// panic says nothing.
func TestSayPanic(t *testing.T) {
	var errOut bytes.Buffer
	sayPanic(&errOut, errors.New("another error"), "panic: no\n")
	sayPanic(&errOut, nil, "")
	if errOut.Len() != 0 {
		t.Fatalf("an end without a panic wrote %q", errOut.String())
	}
	sayPanic(&errOut, tea.ErrProgramPanic, "")
	if !strings.Contains(errOut.String(), "TEA_DEBUG=1") {
		t.Fatalf("a panic with no trace wrote %q", errOut.String())
	}
}
