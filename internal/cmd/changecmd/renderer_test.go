package changecmd

import (
	"bytes"
	"strings"
	"testing"

	"github.com/dantech2000/refresh/internal/change"
	"github.com/dantech2000/refresh/internal/cmdutil"
	"github.com/dantech2000/refresh/internal/health"
)

// With --quiet a report of checks lists the ones that found something. A
// check that passed, and one that was skipped, is in the result of the
// run and not in the lines.
func TestLineRenderer_QuietListsFindingsOnly(t *testing.T) {
	checks := []health.Result{
		{Name: "Nodes", Outcome: health.Pass, Message: "4 of 4 ready"},
		{Name: "Capacity", Outcome: health.Warn, Message: "no CPU metrics"},
		{Name: "DrainBlockers", Outcome: health.Skipped, Message: "no Kubernetes access"},
		{Name: "Workloads", Outcome: health.Unreadable, Message: "could not list the pods"},
	}
	verdict := &health.Verdict{Decision: health.Ask, Reasons: []health.Reason{{Check: "Capacity", Message: "no CPU metrics"}}}
	lines := func(quiet bool, kind change.EventKind) string {
		var errOut bytes.Buffer
		r := NewRenderer(&cmdutil.IOStreams{Out: &bytes.Buffer{}, ErrOut: &errOut, NoColor: true}, quiet)
		r.Render(change.Event{Kind: kind, Checks: checks, Verdict: verdict})
		return errOut.String()
	}
	for _, kind := range []change.EventKind{change.GateReport, change.VerifyReport} {
		loud := lines(false, kind)
		for _, name := range []string{"Nodes", "Capacity", "DrainBlockers", "Workloads"} {
			if !strings.Contains(loud, name) {
				t.Errorf("%s without --quiet lacks the check %s:\n%s", kind, name, loud)
			}
		}
		quiet := lines(true, kind)
		for _, name := range []string{"Capacity", "Workloads"} {
			if !strings.Contains(quiet, name) {
				t.Errorf("%s with --quiet lacks the finding of %s:\n%s", kind, name, quiet)
			}
		}
		for _, name := range []string{"Nodes", "DrainBlockers"} {
			if strings.Contains(quiet, name) {
				t.Errorf("%s with --quiet lists %s, which found nothing:\n%s", kind, name, quiet)
			}
		}
	}
}
