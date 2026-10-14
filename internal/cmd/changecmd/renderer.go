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
	// fleet says that the plan has several clusters: a line about a step
	// then names the cluster of the step.
	fleet bool
	// phases says that the change is an upgrade or a rollback, whose
	// phases run one after another: a line names each as it starts.
	phases bool
	// hold, when set, takes each line in place of stderr. The panel of a
	// roll holds the lines while it is on the screen.
	hold func(line string)
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
	if r.hold != nil {
		r.hold(s)
		return
	}
	_, _ = fmt.Fprintln(r.ios.ErrOut, s)
}

// lines writes several lines, each through line.
func (r *LineRenderer) lines(all []string) {
	for _, s := range all {
		r.line(s)
	}
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
		r.fleet = len(doc.Clusters) > 1
		r.phases = doc.Operation == apidoc.Operation(change.ClusterUpgrade) || doc.Operation == apidoc.Operation(change.ClusterRollback)
		if !r.quiet {
			r.lines(view.PlanLines(th, doc))
		}
		for _, c := range doc.Clusters {
			for _, notice := range c.Notices {
				r.warn(notice)
			}
		}
	case change.GateReport:
		if e.Verdict.Decision != health.Go || len(e.Verdict.Reasons) > 0 {
			r.lines(view.HealthLines(th, health.Documents(r.shown(e.Checks))))
		}
		// A blocker that --force went past is said in words, also with
		// --quiet: the change goes on against a check that failed.
		for _, reason := range e.Verdict.Forced {
			r.warn("--force goes past " + reason.String())
		}
	case change.Notice:
		r.warn(e.Text)
	case change.Progress:
		if !r.quiet {
			r.line(th.Dim(e.Text))
		}
	case change.PhaseStarted:
		// A change with several phases says which one runs.
		if !r.quiet && r.phases {
			r.line(th.Strong(phaseLine(e.Phase)))
		}
	case change.StepChanged:
		if !r.quiet && e.Result.Status != change.StepSkipped {
			doc := stepDoc(*e.Result)
			doc.Name = r.name(e.Result.Ref)
			r.line(view.StepLine(th, doc))
		}
	case change.VerifyReport:
		for _, c := range e.Checks {
			// A check that was skipped found nothing: the result lists it.
			if c.Outcome != health.Pass && c.Outcome != health.Skipped {
				r.lines(view.HealthLines(th, health.Documents(r.shown(e.Checks))))
				break
			}
		}
	case change.UpdatePolled:
		r.poll(e)
	}
}

// shown returns the checks that a report lists. With --quiet those are
// the ones that found something: a warning, a failure, or a read that
// could not be made. A check that passed, and one that was skipped, found
// nothing, and the result of the run still lists every check.
func (r *LineRenderer) shown(checks []health.Result) []health.Result {
	if !r.quiet {
		return checks
	}
	var out []health.Result
	for _, c := range checks {
		if c.Outcome != health.Pass && c.Outcome != health.Skipped {
			out = append(out, c)
		}
	}
	return out
}

// phaseLine names a phase of an upgrade or a rollback as it starts.
func phaseLine(ph change.PhaseRef) string {
	words := map[change.PhaseKind]string{
		change.PhaseControlPlane: "control plane",
		change.PhaseAddonsBefore: "add-ons before the nodegroup rolls",
		change.PhaseNodegroups:   "nodegroups",
		change.PhaseAddonsAfter:  "add-ons",
		change.PhaseAddons:       "add-ons",
	}[ph.Kind]
	if ph.Hop.IsZero() {
		return "phase: " + words
	}
	return "phase: " + words + ", " + ph.Hop.String()
}

// name returns the name of a step for a line: the add-on or the
// nodegroup, with its cluster before it in a fleet.
func (r *LineRenderer) name(ref change.StepRef) string {
	if r.fleet {
		return ref.Phase.Target.Cluster + "/" + ref.Name
	}
	if ref.Kind == change.StepControlPlane {
		return "control-plane"
	}
	return ref.Name
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
	name := r.name(e.Step)
	switch {
	case u.PollError != "":
		r.warn(name + ": could not read the status (" + u.PollError + "); still waiting")
	case changed:
		r.line(r.ios.ErrTheme().Dim(name + ": " + u.Status))
	default:
		r.line(r.ios.ErrTheme().Dim(name + ": still " + u.Status))
	}
}

// stepDoc returns the document form of a step result, for its line.
func stepDoc(s change.StepResult) apidoc.StepResult {
	return apidoc.StepResult{
		Kind: s.Ref.Kind, Name: s.Ref.Name, Hop: s.Ref.Phase.Hop.String(), Status: s.Status,
		Attached: s.Attached, UpdateID: s.UpdateID, InFlight: s.InFlight, Failure: s.Failure, Note: s.Note,
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
