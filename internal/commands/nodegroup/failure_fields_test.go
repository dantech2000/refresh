package nodegroup

import (
	"reflect"
	"testing"

	"github.com/dantech2000/refresh/internal/diag/diagtest"
	"github.com/dantech2000/refresh/internal/services/addons"
	clustersvc "github.com/dantech2000/refresh/internal/services/cluster"
	nodegroupsvc "github.com/dantech2000/refresh/internal/services/nodegroup"
	"github.com/dantech2000/refresh/internal/types"
)

// The -o json/yaml documents that commands still build as map[string]any.
// Each mirror lists the map's keys and value types, so the failure field
// check can see them. REF-178 and REF-179 replace the maps with structs;
// then the struct goes in outputRootTypes and its mirror goes away.
type (
	clusterListEnvelope struct { // cluster list
		Clusters []clustersvc.ClusterSummary `json:"clusters" yaml:"clusters"`
		Count    int                         `json:"count" yaml:"count"`
		Failures []types.RegionFailure       `json:"failures,omitempty" yaml:"failures,omitempty"`
	}
	nodegroupListEnvelope struct { // nodegroup list
		Cluster    string                          `json:"cluster" yaml:"cluster"`
		Nodegroups []nodegroupsvc.NodegroupSummary `json:"nodegroups" yaml:"nodegroups"`
		Count      int                             `json:"count" yaml:"count"`
		Failures   []string                        `json:"failures,omitempty" yaml:"failures,omitempty"`
	}
	addonListEnvelope struct { // addon list
		Cluster  string                `json:"cluster" yaml:"cluster"`
		Addons   []addons.AddonSummary `json:"addons" yaml:"addons"`
		Count    int                   `json:"count" yaml:"count"`
		Failures []string              `json:"failures,omitempty" yaml:"failures,omitempty"`
	}
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
	"nodegroup.clusterListEnvelope.Failures":   "REF-178", // types.RegionFailure
	"nodegroup.nodegroupListEnvelope.Failures": "REF-178", // []string
	"nodegroup.addonListEnvelope.Failures":     "REF-178", // []string
	"status.FleetStatus.Failures":              "REF-178", // types.RegionFailure
	"cluster.ClusterSummary.Warnings":          "REF-178",
	"cluster.ClusterDetails.Warnings":          "REF-178",
	"status.ClusterStatus.Errors":              "REF-178",
	"cluster.UpgradeReport.Incomplete":         "REF-178",
	"nodegroup.updateOutcomes.Failed":          "REF-179",
	"nodegroup.updateOutcomes.RollFailures":    "REF-179",
	"nodegroup.fleetEnvelope.DiscoveryErrors":  "REF-179",
	"upgrade.Plan.Warnings":                    "REF-179", // advisory: becomes notices
}

// TestOutputTypesReportFailuresWithDiag enforces the failure contract of
// docs/concepts/output.md: failures are []diag.Failure under "failures"
// (or one diag.Failure under "failure"), never strings or ad hoc structs.
func TestOutputTypesReportFailuresWithDiag(t *testing.T) {
	roots := append([]reflect.Type{
		reflect.TypeFor[clusterListEnvelope](),
		reflect.TypeFor[nodegroupListEnvelope](),
		reflect.TypeFor[addonListEnvelope](),
		reflect.TypeFor[addonUpdateAllEnvelope](),
		reflect.TypeFor[fleetEnvelope](),
	}, outputRootTypes...)
	diagtest.CheckFailureFields(t, failureFieldAllow, roots...)
}
