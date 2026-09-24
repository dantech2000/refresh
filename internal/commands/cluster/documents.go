package cluster

import (
	"github.com/dantech2000/refresh/internal/apidoc"
	clustersvc "github.com/dantech2000/refresh/internal/services/cluster"
	"github.com/dantech2000/refresh/internal/services/upgrade"
)

// Documents returns a zero value of each document the cluster commands
// print with -o json|yaml, for the JSON Schema generator.
func Documents() []apidoc.Document {
	return []apidoc.Document{
		clustersvc.ClusterList{},
		clustersvc.ClusterDetails{},
		clustersvc.UpgradeReport{},
		clustersvc.InsightDetail{},
		upgrade.Plan{},
		upgradeResult{},
	}
}
