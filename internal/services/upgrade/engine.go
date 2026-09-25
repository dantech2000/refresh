package upgrade

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/dantech2000/refresh/internal/apidoc"
	"github.com/dantech2000/refresh/internal/aws/awserr"
	"github.com/dantech2000/refresh/internal/diag"
)

// ErrAborted is returned by Execute when the user declines a phase
// confirmation. The cluster was not touched by the declined phase.
var ErrAborted = errors.New("upgrade aborted by user")

// ConfirmFunc asks the user to approve a mutating phase; returning false
// aborts the run before the phase starts.
type ConfirmFunc func(prompt string) bool

// ExecuteOptions tunes plan execution.
type ExecuteOptions struct {
	// Yes skips all phase confirmations (--yes).
	Yes bool
	// Confirm prompts before each mutating phase when Yes is false. Required
	// unless Yes is true.
	Confirm ConfirmFunc
	// Progress receives human-readable progress lines.
	Progress ProgressFunc
	// PhaseStart, when set, is called with each mutating phase's label as the
	// phase starts, so the view can draw it as a section header. Without it
	// the label goes to Progress as plain text. The engine never picks glyphs
	// or colors.
	PhaseStart func(label string)
	// SkipAddons / SkipNodegroups mirror the plan options; Force is passed to
	// UpdateNodegroupVersion. All three reach the phase executors.
	SkipAddons     []string
	SkipNodegroups []string
	Force          bool
	// SkipInsightsCheck leaves Cluster Insights out of the live readiness
	// re-gate before each control-plane hop (--skip-insights-check).
	SkipInsightsCheck bool
	// NodegroupGate is an extra pre-roll gate run after the built-in one.
	NodegroupGate NodegroupGate
	// NodegroupObserver, when set, renders a live per-node roll view during each
	// nodegroup roll. Supplied by the command (view) layer; nil → text progress.
	NodegroupObserver RollObserver
}

// Report describes how far an execution got: how it ended, what ran, where
// it stopped, and what remains. Rerunning the same command resumes from
// live cluster state.
type Report struct {
	Status    RunStatus `json:"status" yaml:"status"`
	Completed []string  `json:"completed" yaml:"completed"`
	// StoppedAt is the phase the run stopped in or before, when it stopped.
	StoppedAt string   `json:"stoppedAt,omitempty" yaml:"stoppedAt,omitempty"`
	Remaining []string `json:"remaining" yaml:"remaining"`
	// Failure is why the run stopped, for Failed, Interrupted, and TimedOut
	// runs, and for a Blocked run whose gate could not read what it needed.
	Failure *diag.Failure `json:"failure,omitempty" yaml:"failure,omitempty"`
}

// NewReport returns the report of a run that has not started: status
// Succeeded, and empty lists.
func NewReport() *Report {
	return &Report{Status: RunSucceeded, Completed: []string{}, Remaining: []string{}}
}

// stop records that the run stopped at label with err, leaving remaining
// phases to do. gate is set when err came from the phase's precheck.
func (r *Report) stop(ctx context.Context, cluster, label string, remaining []string, err error, gate bool) {
	r.StoppedAt = label
	if remaining != nil {
		r.Remaining = apidoc.List(remaining)
	}
	r.Status, r.Failure = stopState(ctx, cluster, err, gate)
}

// phase is one confirm-gate-execute unit within a hop.
type phase struct {
	label string
	steps []Step // the plan steps this phase covers (pending ones only)
	// precheck, when set, is a read-only gate evaluated against live state
	// before the confirmation prompt; an error stops the run without
	// starting the phase.
	precheck func(ctx context.Context) error
	run      func(ctx context.Context) error
}

// Execute runs the plan: hops in order, phases (control plane → addons →
// nodegroups) in order within each hop, a confirmation before every mutating
// phase unless opts.Yes, and a halt with a precise completed / failed-at /
// remaining report on the first failure.
//
// Execution state lives in the cluster itself, not in Execute: steps already
// marked completed by BuildPlan are skipped, and each phase executor
// re-checks live state, so rerunning after a failure (or a SIGINT, or a
// complete success) is safe and only performs the remaining work.
func (s *Service) Execute(ctx context.Context, plan *Plan, opts ExecuteOptions) (*Report, error) {
	progress := ensureProgress(opts.Progress)
	report := NewReport()

	if plan.Blocked() {
		report.Status = RunBlocked
		return report, fmt.Errorf("plan has unresolved blockers; refusing to execute:\n  %s",
			joinLines(plan.Blockers()))
	}

	phases := s.phases(plan, opts)

	for i, ph := range phases {
		if len(ph.steps) == 0 {
			continue // nothing pending in this phase
		}

		if ph.precheck != nil {
			if err := ph.precheck(ctx); err != nil {
				report.stop(ctx, plan.ClusterName, ph.label, pendingLabels(phases[i+1:]), err, true)
				if ctx.Err() != nil {
					return report, stopped(ctx, "before "+ph.label, "rerun the same command to resume", err)
				}
				return report, fmt.Errorf("%s not started: %w", ph.label, err)
			}
		}

		if !opts.Yes {
			if opts.Confirm == nil {
				report.Status = RunFailed
				return report, fmt.Errorf("confirmation required for %q but no prompt available (use --yes for non-interactive runs)", ph.label)
			}
			if !opts.Confirm(ph.label) {
				report.stop(ctx, plan.ClusterName, ph.label, pendingLabels(phases[i:]), ErrAborted, false)
				return report, ErrAborted
			}
		}

		if opts.PhaseStart != nil {
			opts.PhaseStart(ph.label)
		} else {
			progress("%s", ph.label)
		}
		if err := ph.run(ctx); err != nil {
			report.stop(ctx, plan.ClusterName, ph.label, pendingLabels(phases[i+1:]), err, false)
			if ctx.Err() != nil {
				// SIGINT / timeout: anything started keeps running
				// server-side; a rerun re-attaches and resumes.
				return report, stopped(ctx, "during "+ph.label, "in-flight EKS updates continue server-side; rerun the same command to resume", err)
			}
			return report, fmt.Errorf("%s failed: %w", ph.label, err)
		}
		report.Completed = append(report.Completed, ph.label)
	}

	return report, nil
}

// stopped wraps err, returned after ctx ended, as a timeout when the run
// deadline passed and as an interrupt otherwise (Ctrl+C, SIGTERM). where
// says when the run stopped ("during <phase>"). next, when not empty, says
// what to do now. The timeout text carries awserr's "increase --timeout"
// hint, which the command retargets to the flag that sets its deadline
// (runner.WaitDeadlineHint names --wait-timeout).
func stopped(ctx context.Context, where, next string, err error) error {
	if !errors.Is(ctx.Err(), context.DeadlineExceeded) {
		if next == "" {
			return fmt.Errorf("interrupted %s: %w", where, err)
		}
		return fmt.Errorf("interrupted %s (%s): %w", where, next, err)
	}
	msg := "timed out " + where
	// An AWS error formatted after the deadline already has the hint.
	if hint := awserr.TimeoutHint(); !strings.Contains(err.Error(), hint) {
		msg += " " + hint
	}
	if next != "" {
		msg += "; " + next
	}
	return fmt.Errorf("%s: %w", msg, err)
}

// phases flattens the plan into the ordered list of executable phases.
func (s *Service) phases(plan *Plan, opts ExecuteOptions) []phase {
	var out []phase
	for _, hop := range plan.Hops {
		var cpSteps, addonSteps, ngSteps []Step
		var ngNames []string
		for _, st := range hop.Steps {
			if st.Status != StatusPending {
				continue
			}
			switch st.Type {
			case StepControlPlane:
				cpSteps = append(cpSteps, st)
			case StepAddon:
				addonSteps = append(addonSteps, st)
			case StepNodegroup:
				ngSteps = append(ngSteps, st)
				ngNames = append(ngNames, st.Target)
			}
		}

		out = append(out, phase{
			label: fmt.Sprintf("control plane %s → %s", hop.From, hop.To),
			steps: cpSteps,
			// Plan-time readiness only saw the cluster as it was then (and
			// EKS insights only cover the next minor), so every hop is
			// re-gated against live state right before its control plane
			// moves.
			precheck: func(ctx context.Context) error {
				return s.checkHopReadiness(ctx, plan.ClusterName, hop.To, opts.SkipInsightsCheck, opts.Progress)
			},
			run: func(ctx context.Context) error {
				return s.UpgradeControlPlane(ctx, plan.ClusterName, hop.To, opts.Progress)
			},
		})
		out = append(out, phase{
			label: fmt.Sprintf("addons for %s (%d update(s), dependency order)", hop.To, len(addonSteps)),
			steps: addonSteps,
			run: func(ctx context.Context) error {
				return s.UpgradeAddons(ctx, plan.ClusterName, hop.To, opts.SkipAddons, opts.Progress)
			},
		})
		out = append(out, phase{
			label: fmt.Sprintf("nodegroup rolls to %s (%d nodegroup(s))", hop.To, len(ngSteps)),
			steps: ngSteps,
			run: func(ctx context.Context) error {
				return s.UpgradeNodegroups(ctx, plan.ClusterName, hop.To, NodegroupRollOptions{
					SkipPatterns: opts.SkipNodegroups,
					Only:         ngNames,
					Force:        opts.Force,
					Gate:         opts.NodegroupGate,
					Observer:     opts.NodegroupObserver,
				}, opts.Progress)
			},
		})
	}
	return out
}

// pendingLabels lists the labels of phases that still have pending steps.
// It is never nil.
func pendingLabels(phases []phase) []string {
	out := []string{}
	for _, ph := range phases {
		if len(ph.steps) > 0 {
			out = append(out, ph.label)
		}
	}
	return out
}

func joinLines(lines []string) string {
	out := ""
	for i, l := range lines {
		if i > 0 {
			out += "\n  "
		}
		out += l
	}
	return out
}
