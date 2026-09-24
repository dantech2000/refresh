package nodegroup

import (
	"reflect"
	"testing"

	"github.com/dantech2000/refresh/internal/diag/diagtest"
	"github.com/dantech2000/refresh/internal/services/addons"
)

// The -o json/yaml documents that commands still build as map[string]any.
// Each mirror lists the map's keys and value types, so the failure field
// check can see them. REF-179 replaces the maps with structs; then the
// struct goes in outputRootTypes and its mirror goes away.
type (
	addonUpdateAllEnvelope struct { // addon update --all
		Cluster string                     `json:"cluster" yaml:"cluster"`
		DryRun  bool                       `json:"dryRun" yaml:"dryRun"`
		Results []addons.AddonUpdateResult `json:"results" yaml:"results"`
	}
	fleetEnvelope struct { // nodegroup update --all-clusters (fleetDocument)
		Clusters        []clusterUpdateResult  `json:"clusters" yaml:"clusters"`
		DiscoveryErrors []regionDiscoveryError `json:"discoveryErrors,omitempty" yaml:"discoveryErrors,omitempty"`
		SkippedRegions  []string               `json:"skippedRegions,omitempty" yaml:"skippedRegions,omitempty"`
	}
)

// failureFieldAllow lists the output fields that still report failures
// without diag.Failure. Each entry names the issue that migrates it, and
// the check fails when an entry is no longer needed, so the migrating PR
// must delete it.
var failureFieldAllow = map[string]string{
	"nodegroup.updateOutcomes.Failed":         "REF-179",
	"nodegroup.updateOutcomes.RollFailures":   "REF-179",
	"nodegroup.fleetEnvelope.DiscoveryErrors": "REF-179",
	"upgrade.Plan.Warnings":                   "REF-179", // advisory: becomes notices
}

// TestOutputTypesReportFailuresWithDiag enforces the failure contract of
// docs/concepts/output.md: failures are []diag.Failure under "failures"
// (or one diag.Failure under "failure"), never strings or ad hoc structs.
func TestOutputTypesReportFailuresWithDiag(t *testing.T) {
	roots := append([]reflect.Type{
		reflect.TypeFor[addonUpdateAllEnvelope](),
		reflect.TypeFor[fleetEnvelope](),
	}, outputRootTypes...)
	diagtest.CheckFailureFields(t, failureFieldAllow, roots...)
}
