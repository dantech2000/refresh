package changecmd

import (
	"fmt"
	"time"

	"github.com/dantech2000/refresh/internal/apidoc"
	"github.com/dantech2000/refresh/internal/change"
	"github.com/dantech2000/refresh/internal/cmdutil"
	"github.com/dantech2000/refresh/internal/health"
	"github.com/dantech2000/refresh/internal/view"
)

// quietInterval is how long a wait may show nothing: a line says that it
// still waits.
const quietInterval = time.Minute

// LineRenderer writes the progress of a run as lines on stderr: the plan
// before a question, a line when a step or the status of its update
// changes, and one a minute while nothing changes. Warnings always print.
// With quiet, nothing else does.
type LineRenderer struct {
	ios   *cmdutil.IOStreams
	quiet bool
	// polls has the last line of each update, and when it was written.
	polls map[change.StepRef]pollLine
}

type pollLine struct {
	status, pollError string
	at                time.Time
}

// NewRenderer returns the renderer of a command that has no live panel.
// The lines go to stderr in every format, so stdout holds the result
// alone.
func NewRenderer(ios *cmdutil.IOStreams, quiet bool) *LineRenderer {
	return &LineRenderer{ios: ios, quiet: quiet, polls: map[change.StepRef]pollLine{}}
}

// line writes one line to stderr. Nothing can report a failed write
// there.
func (r *LineRenderer) line(s string) {
	_, _ = fmt.Fprintln(r.ios.ErrOut, s)
}

func (r *LineRenderer) warn(s string) {
	r.line(r.ios.ErrTheme().Caution("warning: " + s))
}

// Render implements Renderer.
func (r *LineRenderer) Render(e change.Event) {
	th := r.ios.ErrTheme()
	switch e.Kind {
	case change.PlanReady:
		doc := e.Plan.Document()
		if !r.quiet {
			_ = cmdutil.WriteLines(r.ios.ErrOut, view.PlanLines(th, doc))
		}
		for _, c := range doc.Clusters {
			for _, notice := range c.Notices {
				r.warn(notice)
			}
		}
	case change.GateReport:
		if e.Verdict.Decision != health.Go || len(e.Verdict.Reasons) > 0 {
			_ = cmdutil.WriteLines(r.ios.ErrOut, view.HealthLines(th, health.Documents(e.Checks)))
		}
		// A blocker that --force went past is said in words, also with
		// --quiet: the change goes on against a check that failed.
		for _, reason := range e.Verdict.Forced {
			r.warn("--force goes past " + reason.String())
		}
	case change.Notice:
		r.warn(e.Text)
	case change.StepChanged:
		if !r.quiet && e.Result.Status != change.StepSkipped {
			r.line(view.StepLine(th, stepDoc(*e.Result)))
		}
	case change.VerifyReport:
		for _, c := range e.Checks {
			if c.Outcome != health.Pass {
				_ = cmdutil.WriteLines(r.ios.ErrOut, view.HealthLines(th, health.Documents(e.Checks)))
				break
			}
		}
	case change.UpdatePolled:
		r.poll(e)
	}
}

// poll writes the line of a poll when it says something new, or when the
// last line of the update is a minute old.
func (r *LineRenderer) poll(e change.Event) {
	// The poll that ends a wait says what the line of the step says next.
	if r.quiet || e.Update.Final {
		return
	}
	u, last := e.Update, r.polls[e.Step]
	changed := u.Status != last.status || u.PollError != last.pollError
	if !changed && e.At.Sub(last.at) < quietInterval {
		return
	}
	r.polls[e.Step] = pollLine{status: u.Status, pollError: u.PollError, at: e.At}
	switch {
	case u.PollError != "":
		r.warn(e.Step.Name + ": could not read the status (" + u.PollError + "); still waiting")
	case changed:
		r.line(r.ios.ErrTheme().Dim(e.Step.Name + ": " + u.Status))
	default:
		r.line(r.ios.ErrTheme().Dim(e.Step.Name + ": still " + u.Status))
	}
}

// stepDoc returns the document form of a step result, for its line.
func stepDoc(s change.StepResult) apidoc.StepResult {
	return apidoc.StepResult{
		Kind: s.Ref.Kind, Name: s.Ref.Name, Status: s.Status,
		Attached: s.Attached, UpdateID: s.UpdateID, InFlight: s.InFlight, Failure: s.Failure,
	}
}

// Pause implements Renderer. Lines need no pause: a prompt is one more
// line.
func (*LineRenderer) Pause() {}

// Resume implements Renderer.
func (*LineRenderer) Resume() {}

// Close implements Renderer.
func (*LineRenderer) Close() {}

// View implements Renderer: the view of the document that the result
// prints.
func (*LineRenderer) View(res change.Result) cmdutil.HumanView {
	switch doc := res.Document().(type) {
	case apidoc.Plan:
		return view.PlanView(doc)
	case apidoc.Run:
		return view.RunView(doc)
	}
	return view.Description{}
}
