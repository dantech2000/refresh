package ui

import (
	"fmt"
)

// RegionTreeBuilder specializes in multi-region organization
type RegionTreeBuilder struct {
	builder *TreeBuilder
}

// NewRegionTreeBuilder creates a specialized region organization builder
func NewRegionTreeBuilder() *RegionTreeBuilder {
	return &RegionTreeBuilder{builder: NewTreeBuilder()}
}

// AddRegion adds a region root node
func (rtb *RegionTreeBuilder) AddRegion(name string, clusterCount int) *RegionTreeBuilder {
	rtb.builder.AddRoot("REGION " + fmt.Sprintf("%s (%d clusters)", name, clusterCount))
	return rtb
}

// AddClusterToRegion adds a cluster to the current region. Clusters always
// nest one level beneath their region header (AddRoot resets the level to 0,
// so relying on the current level would render clusters as region siblings).
func (rtb *RegionTreeBuilder) AddClusterToRegion(name, status string, nodeCount int32) *RegionTreeBuilder {
	rtb.builder.level = 1
	rtb.builder.AddStatus("CLUSTER", fmt.Sprintf("%s (%s, %d nodes)", name, status, nodeCount), status)
	return rtb
}

// FinishRegion moves back to the root level for next region
func (rtb *RegionTreeBuilder) FinishRegion() *RegionTreeBuilder {
	rtb.builder.UpTo(0)
	return rtb
}

// RenderWithTitle outputs the region tree with a title
func (rtb *RegionTreeBuilder) RenderWithTitle(title string) error {
	return rtb.builder.RenderWithTitle(title)
}
