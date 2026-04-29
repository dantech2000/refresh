package ui

import (
	"fmt"
	"strings"
)

// ClusterTreeBuilder specializes in EKS cluster hierarchy visualization
type ClusterTreeBuilder struct {
	builder *TreeBuilder
}

// RegionTreeBuilder specializes in multi-region organization
type RegionTreeBuilder struct {
	builder *TreeBuilder
}

// ComparisonTreeBuilder specializes in cluster comparison differences
type ComparisonTreeBuilder struct {
	builder *TreeBuilder
}

// NewClusterTreeBuilder creates a specialized cluster hierarchy builder
func NewClusterTreeBuilder() *ClusterTreeBuilder {
	return &ClusterTreeBuilder{builder: NewTreeBuilder()}
}

// NewRegionTreeBuilder creates a specialized region organization builder
func NewRegionTreeBuilder() *RegionTreeBuilder {
	return &RegionTreeBuilder{builder: NewTreeBuilder()}
}

// NewComparisonTreeBuilder creates a specialized comparison differences builder
func NewComparisonTreeBuilder() *ComparisonTreeBuilder {
	return &ComparisonTreeBuilder{builder: NewTreeBuilder()}
}

// ClusterTreeBuilder methods

// AddCluster adds a cluster root node
func (ctb *ClusterTreeBuilder) AddCluster(name, status, version string, nodeCount int) *ClusterTreeBuilder {
	ctb.builder.AddRoot("CLUSTER " + fmt.Sprintf("%s (%s, %s, %d nodes)", name, status, version, nodeCount))
	return ctb
}

// AddNodegroup adds a nodegroup to the current cluster
func (ctb *ClusterTreeBuilder) AddNodegroup(name, status, instanceType string, readyNodes, desiredNodes int32) *ClusterTreeBuilder {
	ctb.builder.AddChild("NODEGROUP " + fmt.Sprintf("%s (%s, %s, %d/%d nodes)", name, status, instanceType, readyNodes, desiredNodes))
	return ctb
}

// AddInstance adds an instance to the current nodegroup
func (ctb *ClusterTreeBuilder) AddInstance(instanceId, state, az string) *ClusterTreeBuilder {
	ctb.builder.AddChild("INSTANCE " + fmt.Sprintf("%s (%s, %s)", instanceId, state, az))
	return ctb
}

// AddAddon adds an addon to the cluster
func (ctb *ClusterTreeBuilder) AddAddon(name, version, status string) *ClusterTreeBuilder {
	ctb.builder.AddStatus("ADDON", fmt.Sprintf("%s (%s)", name, version), status)
	return ctb
}

// FinishNodegroup moves back to cluster level
func (ctb *ClusterTreeBuilder) FinishNodegroup() *ClusterTreeBuilder {
	ctb.builder.UpTo(1)
	return ctb
}

// Render outputs the cluster tree
func (ctb *ClusterTreeBuilder) Render() error { return ctb.builder.Render() }

// RenderWithTitle outputs the cluster tree with a title
func (ctb *ClusterTreeBuilder) RenderWithTitle(title string) error {
	return ctb.builder.RenderWithTitle(title)
}

// RegionTreeBuilder methods

// AddRegion adds a region root node
func (rtb *RegionTreeBuilder) AddRegion(name string, clusterCount int) *RegionTreeBuilder {
	rtb.builder.AddRoot("REGION " + fmt.Sprintf("%s (%d clusters)", name, clusterCount))
	return rtb
}

// AddClusterToRegion adds a cluster to the current region
func (rtb *RegionTreeBuilder) AddClusterToRegion(name, status string, nodeCount int32) *RegionTreeBuilder {
	rtb.builder.AddStatus("CLUSTER", fmt.Sprintf("%s (%s, %d nodes)", name, status, nodeCount), status)
	return rtb
}

// FinishRegion moves back to the root level for next region
func (rtb *RegionTreeBuilder) FinishRegion() *RegionTreeBuilder {
	rtb.builder.UpTo(0)
	return rtb
}

// Render outputs the region tree
func (rtb *RegionTreeBuilder) Render() error { return rtb.builder.Render() }

// RenderWithTitle outputs the region tree with a title
func (rtb *RegionTreeBuilder) RenderWithTitle(title string) error {
	return rtb.builder.RenderWithTitle(title)
}

// ComparisonTreeBuilder methods

// AddComparisonRoot adds a comparison root node
func (ctb *ComparisonTreeBuilder) AddComparisonRoot(clusters []string) *ComparisonTreeBuilder {
	ctb.builder.AddRoot("COMPARISON Cluster Comparison: " + strings.Join(clusters, " vs "))
	return ctb
}

// AddDifferenceCategory adds a category of differences
func (ctb *ComparisonTreeBuilder) AddDifferenceCategory(category string) *ComparisonTreeBuilder {
	var prefix string
	switch strings.ToLower(category) {
	case "configuration":
		prefix = "CONFIG"
	case "networking":
		prefix = "NETWORK"
	case "security":
		prefix = "SECURITY"
	case "nodegroups":
		prefix = "NODEGROUPS"
	case "addons":
		prefix = "ADDONS"
	default:
		prefix = "CATEGORY"
	}
	ctb.builder.AddChild(prefix + " " + category)
	return ctb
}

// AddDifference adds a specific difference
func (ctb *ComparisonTreeBuilder) AddDifference(field string, values []string, severity string) *ComparisonTreeBuilder {
	ctb.builder.AddStatus("", fmt.Sprintf("%s: %s", field, strings.Join(values, " vs ")), severity)
	return ctb
}

// AddSimilarity adds a similarity (no difference)
func (ctb *ComparisonTreeBuilder) AddSimilarity(field, value string) *ComparisonTreeBuilder {
	ctb.builder.AddStatus("", fmt.Sprintf("%s: %s (both)", field, value), "SUCCESS")
	return ctb
}

// FinishCategory moves back to comparison root level
func (ctb *ComparisonTreeBuilder) FinishCategory() *ComparisonTreeBuilder {
	ctb.builder.UpTo(1)
	return ctb
}

// Render outputs the comparison tree
func (ctb *ComparisonTreeBuilder) Render() error { return ctb.builder.Render() }

// RenderWithTitle outputs the comparison tree with a title
func (ctb *ComparisonTreeBuilder) RenderWithTitle(title string) error {
	return ctb.builder.RenderWithTitle(title)
}
