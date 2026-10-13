package health

import (
	"fmt"
	"slices"
	"strings"

	"github.com/dantech2000/refresh/internal/apidoc"
	"github.com/dantech2000/refresh/internal/cmdutil"
)

// Decision is the verdict of the gate.
type Decision string

// The decisions.
const (
	Go    Decision = "Go"
	Ask   Decision = "Ask"
	Block Decision = "Block"
)

// Policy is what the flags of a changing command say to the gate.
type Policy struct {
	// Yes: an Ask becomes Go, and its reasons go in Verdict.Accepted.
	Yes bool
	// RequireHealthy: an Ask becomes Block. It wins over Yes.
	RequireHealthy bool
	// Force: a Fail or an Unreadable of a Forceable check becomes a
	// warning, and its reason goes in Verdict.Forced.
	Force bool
	// Skip: no check ran. The verdict is Go with Skipped set.
	Skip bool
}

// Reason is one check behind a verdict.
type Reason struct {
	Check   string
	Outcome Outcome
	Message string
}

func (r Reason) String() string { return r.Check + ": " + r.Message }

// Verdict is what the gate decided and why.
type Verdict struct {
	Decision Decision
	// Exit is the code of the findings, whatever the policy: 4 when a
	// check is Unreadable, else 3 when a blocking check fails and is not
	// forced, else 2 when there is any warning, else 0. A dry run exits
	// with it, and so does a blocked run.
	Exit int
	// Reasons are the results that are not a pass, worst first. Blockers
	// are the ones that block: a blocking Fail, or an Unreadable of a
	// check that fails closed, when --force did not override it. Accepted
	// are the reasons that --yes went past, and Forced the ones that
	// --force went past. Skipped is set when no check ran.
	Reasons  []Reason
	Blockers []Reason
	Accepted []Reason
	Forced   []Reason
	Skipped  bool
	// Failures are the reads that the checks could not make. A run that
	// goes on keeps them and exits 4 at its end.
	Failures []apidoc.Failure
}

// The ranks of a result. The worst rank decides.
const (
	rankBlock   = 1 // a blocking Fail
	rankClosed  = 2 // an Unreadable of a check that fails closed
	rankWarning = 3 // anything else that is not a pass
	rankPass    = 4
)

// Gate folds results into a verdict under p.
//
// --force is applied first: each Forceable result that is Fail or
// Unreadable counts as a warning, and keeps its failures. Then every
// result gets a rank: a blocking Fail, then an Unreadable of a check that
// fails closed, then every other Fail, Unreadable, or Warn. A Skipped
// result and a Pass have no rank. The worst rank decides: Block, Block,
// or Ask. An Ask becomes Block with --require-healthy, which wins, and Go
// with --yes.
func Gate(results []Result, p Policy) Verdict {
	v := Verdict{Decision: Go, Skipped: p.Skip}
	if p.Skip {
		return v
	}
	type ranked struct {
		Reason
		rank   int
		forced bool
	}
	var all []ranked
	worst := rankPass
	for _, r := range results {
		v.Failures = append(v.Failures, r.Failures...)
		forced := p.Force && r.Forceable && (r.Outcome == Fail || r.Outcome == Unreadable)
		rank := rankOf(r, forced)
		if rank == rankPass {
			continue
		}
		all = append(all, ranked{Reason{r.Name, r.Outcome, r.Message}, rank, forced})
		worst = min(worst, rank)
	}
	slices.SortStableFunc(all, func(a, b ranked) int { return a.rank - b.rank })
	for _, r := range all {
		v.Reasons = append(v.Reasons, r.Reason)
		if r.rank < rankWarning {
			v.Blockers = append(v.Blockers, r.Reason)
		}
		if r.forced {
			v.Forced = append(v.Forced, r.Reason)
		}
	}
	v.Exit = exitOf(results, all, func(r ranked) int { return r.rank })
	switch worst {
	case rankBlock, rankClosed:
		v.Decision = Block
	case rankWarning:
		switch {
		case p.RequireHealthy:
			v.Decision = Block
		case p.Yes:
			v.Decision = Go
			v.Accepted = slices.Clone(v.Reasons)
		default:
			v.Decision = Ask
		}
	}
	return v
}

// rankOf returns the rank of a result. A forced result ranks as a warning.
func rankOf(r Result, forced bool) int {
	switch r.Outcome {
	case Fail:
		if r.Blocking && !forced {
			return rankBlock
		}
		return rankWarning
	case Unreadable:
		if r.FailClosed && !forced {
			return rankClosed
		}
		return rankWarning
	case Warn:
		return rankWarning
	}
	return rankPass
}

// exitOf returns the exit code of the findings, before the policy.
func exitOf[T any](results []Result, ranked []T, rank func(T) int) int {
	for _, r := range results {
		if r.Outcome == Unreadable {
			return cmdutil.ExitIncomplete
		}
	}
	code := cmdutil.ExitOK
	for _, r := range ranked {
		switch rank(r) {
		case rankBlock, rankClosed:
			return cmdutil.ExitBlocked
		case rankWarning:
			code = cmdutil.ExitNeedsAttention
		}
	}
	return code
}

// Question is the prompt of an Ask, such as "proceed despite 2 warnings
// (Nodes: 5/6 nodes ready; Quotas: 91% of the vCPU quota in use)?".
func (v Verdict) Question() string {
	return fmt.Sprintf("proceed despite %s (%s)?", plural(len(v.Reasons), "warning"), joinReasons(v.Reasons))
}

// Summary is the one-line error of a Block: the blockers, or the warnings
// that --require-healthy turned into a stop.
func (v Verdict) Summary() string {
	if len(v.Blockers) == 0 {
		return fmt.Sprintf("health checks reported %s and --require-healthy is set: %s", plural(len(v.Reasons), "warning"), joinReasons(v.Reasons))
	}
	return fmt.Sprintf("blocked by %s: %s", plural(len(v.Blockers), "health check"), joinReasons(v.Blockers))
}

func joinReasons(reasons []Reason) string {
	parts := make([]string, len(reasons))
	for i, r := range reasons {
		parts[i] = r.String()
	}
	return strings.Join(parts, "; ")
}

func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

// Worse returns the checks of cur that are worse than in prev: a worse
// outcome, a finding that prev did not have, or a check that ran in prev
// and is Skipped in cur. A check that measures a number and keeps its
// outcome is not worse, whatever its numbers. A check that is new in cur
// is not worse either.
func Worse(prev, cur []Result) []Reason {
	before := map[string]Result{}
	for _, r := range prev {
		before[r.Name] = r
	}
	var out []Reason
	for _, r := range cur {
		p, ok := before[r.Name]
		if !ok {
			continue
		}
		switch {
		case r.Outcome == Skipped && p.Outcome != Skipped:
			out = append(out, Reason{r.Name, r.Outcome, "did not run this time: " + r.Message})
		case severity(r.Outcome) > severity(p.Outcome):
			out = append(out, Reason{r.Name, r.Outcome, r.Message})
		case severity(r.Outcome) == severity(p.Outcome) && newFinding(p.Findings, r.Findings):
			out = append(out, Reason{r.Name, r.Outcome, r.Message})
		}
	}
	return out
}

// newFinding reports whether cur has a finding that prev does not.
func newFinding(prev, cur []string) bool {
	for _, f := range cur {
		if !slices.Contains(prev, f) {
			return true
		}
	}
	return false
}

// GateDoc returns the document form of a verdict.
func GateDoc(v Verdict, results []Result) apidoc.Gate {
	return apidoc.Gate{
		Decision: apidoc.GateDecision(v.Decision),
		Reasons:  reasonStrings(v.Reasons),
		Accepted: reasonStrings(v.Accepted),
		Forced:   reasonStrings(v.Forced),
		Checks:   Documents(results),
	}
}

func reasonStrings(reasons []Reason) []string {
	out := make([]string, len(reasons))
	for i, r := range reasons {
		out[i] = r.String()
	}
	return out
}
