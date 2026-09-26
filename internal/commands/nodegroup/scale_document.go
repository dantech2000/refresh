package nodegroup

import (
	"github.com/aws/aws-sdk-go-v2/aws"
	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"

	"github.com/dantech2000/refresh/internal/apidoc"
	"github.com/dantech2000/refresh/internal/diag"
	nodegroupsvc "github.com/dantech2000/refresh/internal/services/nodegroup"
)

// scaleOutcome is what `nodegroup scale` did. docs/commands/nodegroup.md
// documents the values; later versions may add values.
type scaleOutcome string

const (
	// scalePlanned: a dry run; nothing changed.
	scalePlanned scaleOutcome = "Planned"
	// scaleRequested: EKS accepted the scaling request, and the run did
	// not wait for it (no --wait).
	scaleRequested scaleOutcome = "Requested"
	// scaleCompleted: with --wait, the scale settled at the requested
	// sizes.
	scaleCompleted scaleOutcome = "Completed"
	// scaleBlocked: the --check-pdbs gate or the pre-scaling health check
	// refused the scale. Nothing changed. Exit 3.
	scaleBlocked scaleOutcome = "Blocked"
	// scaleCompletedWithIssues: the scale was applied, but the
	// post-scaling health check found blocking issues. Exit 5.
	scaleCompletedWithIssues scaleOutcome = "CompletedWithIssues"
)

// EnumValues lists every scaleOutcome.
func (scaleOutcome) EnumValues() []string {
	return []string{
		string(scalePlanned), string(scaleRequested), string(scaleCompleted),
		string(scaleBlocked), string(scaleCompletedWithIssues),
	}
}

// pdbGateResult is the verdict of the --check-pdbs gate.
type pdbGateResult string

const (
	// pdbGateNotScaleDown: the desired size does not go down, so nothing
	// was checked.
	pdbGateNotScaleDown pdbGateResult = "NotScaleDown"
	// pdbGatePassed: no PodDisruptionBudget blocks the scale-down.
	pdbGatePassed pdbGateResult = "Passed"
	// pdbGateRefused: blockers refuse the scale-down (without --force).
	pdbGateRefused pdbGateResult = "Refused"
	// pdbGateOverridden: blockers were found, and --force scales anyway.
	pdbGateOverridden pdbGateResult = "Overridden"
	// pdbGateUnchecked: the PDBs could not be read, and --force scales
	// without the gate (see failures).
	pdbGateUnchecked pdbGateResult = "Unchecked"
)

// EnumValues lists every pdbGateResult.
func (pdbGateResult) EnumValues() []string {
	return []string{
		string(pdbGateNotScaleDown), string(pdbGatePassed), string(pdbGateRefused),
		string(pdbGateOverridden), string(pdbGateUnchecked),
	}
}

// scaleSizes is a nodegroup's desired/min/max size.
type scaleSizes struct {
	Desired int32 `json:"desired" yaml:"desired"`
	Min     int32 `json:"min" yaml:"min"`
	Max     int32 `json:"max" yaml:"max"`
}

// scalePDBGate is the --check-pdbs verdict.
type scalePDBGate struct {
	Result pdbGateResult `json:"result" yaml:"result"`
	// Blockers name the PodDisruptionBudgets that the scale-down could take
	// below their budget.
	Blockers []string `json:"blockers" yaml:"blockers"`
	// Scoped is false when the blockers could not be narrowed to the
	// nodegroup's nodes: they are then every at-risk PDB in the cluster.
	Scoped bool `json:"scoped" yaml:"scoped"`
	// Note explains a short-circuit in the check, if any.
	Note string `json:"note,omitempty" yaml:"note,omitempty"`
}

// scaleDocument is the -o json/yaml document of `nodegroup scale`.
type scaleDocument struct {
	Cluster   string       `json:"cluster" yaml:"cluster"`
	Nodegroup string       `json:"nodegroup" yaml:"nodegroup"`
	Region    string       `json:"region" yaml:"region"`
	Outcome   scaleOutcome `json:"outcome" yaml:"outcome"`
	DryRun    bool         `json:"dryRun" yaml:"dryRun"`
	// Before is the scaling config before the run; After is what the run
	// asks for (Before with the requested sizes).
	Before scaleSizes `json:"before" yaml:"before"`
	After  scaleSizes `json:"after" yaml:"after"`
	// Waited is true with --wait.
	Waited bool `json:"waited" yaml:"waited"`
	// NodegroupStatus is the nodegroup's EKS status after a --wait that
	// completed.
	NodegroupStatus string `json:"nodegroupStatus,omitempty" yaml:"nodegroupStatus,omitempty"`
	// PDBGate is the --check-pdbs verdict, when the gate ran.
	PDBGate  *scalePDBGate `json:"pdbGate,omitempty" yaml:"pdbGate,omitempty"`
	Failures diag.List     `json:"failures" yaml:"failures"`
}

// DocumentKind is NodegroupScale.
func (scaleDocument) DocumentKind() apidoc.Kind { return apidoc.KindNodegroupScale }

// sizesOf reads a scaling config.
func sizesOf(sc ekstypes.NodegroupScalingConfig) scaleSizes {
	return scaleSizes{Desired: aws.ToInt32(sc.DesiredSize), Min: aws.ToInt32(sc.MinSize), Max: aws.ToInt32(sc.MaxSize)}
}

// withRequested is s with the requested sizes set.
func (s scaleSizes) withRequested(desired, minSize, maxSize *int32) scaleSizes {
	if desired != nil {
		s.Desired = *desired
	}
	if minSize != nil {
		s.Min = *minSize
	}
	if maxSize != nil {
		s.Max = *maxSize
	}
	return s
}

// pdbGateOf is the gate verdict from a check the command ran (a dry run,
// or --force), or nil when the gate did not run.
func pdbGateOf(check *nodegroupsvc.ScaleDownPDBCheck, checkErr error, force bool) *scalePDBGate {
	switch {
	case checkErr != nil:
		// Without --force the run fails closed (exit 1) with no document.
		return &scalePDBGate{Result: pdbGateUnchecked, Blockers: []string{}}
	case check == nil:
		return nil
	}
	g := pdbGateFromCheck(*check)
	if g.Result == pdbGateRefused && force {
		g.Result = pdbGateOverridden
	}
	return g
}

// pdbGateFromCheck is the verdict of check, without --force.
func pdbGateFromCheck(check nodegroupsvc.ScaleDownPDBCheck) *scalePDBGate {
	g := &scalePDBGate{Result: pdbGatePassed, Blockers: []string{}, Scoped: check.Scoped, Note: check.Note}
	switch {
	case !check.ScaleDown:
		g.Result = pdbGateNotScaleDown
	case check.Refused():
		g.Result = pdbGateRefused
	}
	for _, p := range check.Blockers {
		g.Blockers = append(g.Blockers, p.DrainBlockerSummary())
	}
	return g
}
