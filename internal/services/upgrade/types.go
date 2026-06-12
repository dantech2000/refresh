// Package upgrade implements the cluster upgrade orchestrator: plan
// generation, the control-plane / addon / nodegroup phases, and the
// sequencing engine that runs a plan with health gates between phases.
//
// EKS upgrades one minor version at a time, so a multi-minor upgrade expands
// into sequential "hops" (1.31 → 1.32 → 1.33). Each hop runs control plane →
// addons (dependency order, versions compatible with the hop target) →
// nodegroup rolls, with a gate after every phase.
//
// The orchestrator is resumable by re-derivation rather than state files:
// BuildPlan inspects actual cluster state and marks already-satisfied steps
// completed, so rerunning `refresh cluster upgrade --to X` after a failure
// (or after success) only executes what remains.
package upgrade

import (
	"fmt"
	"strconv"
	"strings"
)

// StepType identifies which phase a plan step belongs to.
type StepType string

const (
	StepReadiness    StepType = "readiness"
	StepControlPlane StepType = "control-plane"
	StepAddon        StepType = "addon"
	StepNodegroup    StepType = "nodegroup"
)

// StepStatus is the planned/derived state of a step.
type StepStatus string

const (
	// StatusPending means the step still needs to run.
	StatusPending StepStatus = "pending"
	// StatusCompleted means live cluster state already satisfies the step,
	// so the engine skips it (this is what makes reruns resumable/no-ops).
	StatusCompleted StepStatus = "completed"
	// StatusBlocked means the step cannot run until the blocker is resolved;
	// a plan containing blocked steps refuses to execute.
	StatusBlocked StepStatus = "blocked"
	// StatusManual means the orchestrator will not perform this step (e.g.
	// custom-AMI nodegroups); it is surfaced for the operator instead.
	StatusManual StepStatus = "manual"
)

// Step is one entry in the ordered upgrade plan.
type Step struct {
	Type        StepType   `json:"type" yaml:"type"`
	Description string     `json:"description" yaml:"description"`
	Target      string     `json:"target,omitempty" yaml:"target,omitempty"`
	Version     string     `json:"version,omitempty" yaml:"version,omitempty"`
	Status      StepStatus `json:"status" yaml:"status"`
	Reason      string     `json:"reason,omitempty" yaml:"reason,omitempty"`
}

// Hop is a single minor-version upgrade cycle within the plan.
type Hop struct {
	From  string `json:"from" yaml:"from"`
	To    string `json:"to" yaml:"to"`
	Steps []Step `json:"steps" yaml:"steps"`
}

// Plan is the full ordered upgrade plan for a cluster.
type Plan struct {
	ClusterName    string   `json:"clusterName" yaml:"clusterName"`
	CurrentVersion string   `json:"currentVersion" yaml:"currentVersion"`
	TargetVersion  string   `json:"targetVersion" yaml:"targetVersion"`
	Hops           []Hop    `json:"hops" yaml:"hops"`
	Warnings       []string `json:"warnings,omitempty" yaml:"warnings,omitempty"`
}

// Blockers returns the descriptions of all blocked steps across hops.
func (p *Plan) Blockers() []string {
	var out []string
	for _, hop := range p.Hops {
		for _, s := range hop.Steps {
			if s.Status == StatusBlocked {
				out = append(out, fmt.Sprintf("%s → %s: %s: %s", hop.From, hop.To, s.Description, s.Reason))
			}
		}
	}
	return out
}

// Blocked reports whether any step in the plan is blocked.
func (p *Plan) Blocked() bool { return len(p.Blockers()) > 0 }

// PendingSteps counts mutating steps the engine would actually execute.
// Readiness steps are checks, not work, so they don't count.
func (p *Plan) PendingSteps() int {
	n := 0
	for _, hop := range p.Hops {
		for _, s := range hop.Steps {
			if s.Status == StatusPending && s.Type != StepReadiness {
				n++
			}
		}
	}
	return n
}

// kubeletSkew is the supported kubelet version skew: nodes may lag the
// control plane by at most this many minor versions (Kubernetes >= 1.28).
const kubeletSkew = 3

// minorVersion parses an EKS Kubernetes version string ("1.31") into its
// minor component, validating the major is 1.
func minorVersion(v string) (int, error) {
	parts := strings.SplitN(strings.TrimSpace(v), ".", 3)
	if len(parts) < 2 || parts[0] != "1" {
		return 0, fmt.Errorf("unrecognized Kubernetes version %q (expected \"1.<minor>\")", v)
	}
	minor, err := strconv.Atoi(parts[1])
	if err != nil {
		return 0, fmt.Errorf("unrecognized Kubernetes version %q: %w", v, err)
	}
	return minor, nil
}

// expandHops lists the sequential minor-version targets between from and to,
// exclusive of from, inclusive of to ("1.31"→"1.33" yields ["1.32","1.33"]).
// to == from yields zero hops — that's how a rerun of a completed upgrade
// becomes a no-op rather than an error.
func expandHops(from, to string) ([]string, error) {
	fromMinor, err := minorVersion(from)
	if err != nil {
		return nil, err
	}
	toMinor, err := minorVersion(to)
	if err != nil {
		return nil, err
	}
	if toMinor < fromMinor {
		return nil, fmt.Errorf("target version %s is older than current version %s", to, from)
	}
	var hops []string
	for m := fromMinor + 1; m <= toMinor; m++ {
		hops = append(hops, fmt.Sprintf("1.%d", m))
	}
	return hops, nil
}

// versionAtLeast reports whether version a >= b (minor comparison).
func versionAtLeast(a, b string) bool {
	am, err1 := minorVersion(a)
	bm, err2 := minorVersion(b)
	if err1 != nil || err2 != nil {
		return false
	}
	return am >= bm
}
