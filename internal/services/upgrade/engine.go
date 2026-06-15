package upgrade

import (
	"context"
	"errors"
	"fmt"
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
	// SkipAddons / SkipNodegroups / Force mirror the plan options and are
	// passed through to the phase executors.
	SkipAddons     []string
	SkipNodegroups []string
	Force          bool
	// NodegroupGate overrides the built-in pre-roll health gate.
	NodegroupGate NodegroupGate
	// NodegroupObserver, when set, renders a live per-node roll view during each
	// nodegroup roll. Supplied by the command (view) layer; nil → text progress.
	NodegroupObserver RollObserver
}

// Report describes how far an execution got: what ran, where it stopped, and
// what remains. Rerunning the same command resumes from live cluster state.
type Report struct {
	Completed []string `json:"completed,omitempty" yaml:"completed,omitempty"`
	FailedAt  string   `json:"failedAt,omitempty" yaml:"failedAt,omitempty"`
	Remaining []string `json:"remaining,omitempty" yaml:"remaining,omitempty"`
}

// phase is one confirm-gate-execute unit within a hop.
type phase struct {
	label string
	steps []Step // the plan steps this phase covers (pending ones only)
	run   func(ctx context.Context) error
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
	report := &Report{}

	if plan.Blocked() {
		return report, fmt.Errorf("plan has unresolved blockers; refusing to execute:\n  %s",
			joinLines(plan.Blockers()))
	}

	phases := s.phases(plan, opts)

	for i, ph := range phases {
		if len(ph.steps) == 0 {
			continue // nothing pending in this phase
		}

		if !opts.Yes {
			if opts.Confirm == nil {
				return report, fmt.Errorf("confirmation required for %q but no prompt available (use --yes for non-interactive runs)", ph.label)
			}
			if !opts.Confirm(ph.label) {
				report.Remaining = pendingLabels(phases[i:])
				return report, ErrAborted
			}
		}

		progress("▸ %s", ph.label)
		if err := ph.run(ctx); err != nil {
			report.FailedAt = ph.label
			report.Remaining = pendingLabels(phases[i+1:])
			if ctx.Err() != nil {
				// SIGINT / timeout: anything started keeps running
				// server-side; a rerun re-attaches and resumes.
				return report, fmt.Errorf("interrupted during %s (in-flight EKS updates continue server-side; rerun the same command to resume): %w", ph.label, err)
			}
			return report, fmt.Errorf("%s failed: %w", ph.label, err)
		}
		report.Completed = append(report.Completed, ph.label)
	}

	return report, nil
}

// phases flattens the plan into the ordered list of executable phases.
func (s *Service) phases(plan *Plan, opts ExecuteOptions) []phase {
	var out []phase
	for _, hop := range plan.Hops {
		hop := hop
		var cpSteps, addonSteps, ngSteps []Step
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
			}
		}

		out = append(out, phase{
			label: fmt.Sprintf("control plane %s → %s", hop.From, hop.To),
			steps: cpSteps,
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
func pendingLabels(phases []phase) []string {
	var out []string
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
