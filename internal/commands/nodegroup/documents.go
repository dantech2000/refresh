package nodegroup

import (
	"github.com/dantech2000/refresh/internal/apidoc"
	"github.com/dantech2000/refresh/internal/health"
	nodegroupsvc "github.com/dantech2000/refresh/internal/services/nodegroup"
)

// Documents returns a zero value of each document the nodegroup commands
// print with -o json|yaml, for the JSON Schema generator.
func Documents() []apidoc.Document {
	return []apidoc.Document{
		nodegroupsvc.NodegroupList{},
		nodegroupsvc.NodegroupDetails{},
		updateDocument{},
		dryRunPlan{},
		fleetUpdateDocument{},
		fleetDryRunDocument{},
		health.HealthSummary{},
	}
}
