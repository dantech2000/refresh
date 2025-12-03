package ui

import (
	"fmt"
	"strings"

	"github.com/fatih/color"
	"github.com/pterm/pterm"
	"github.com/pterm/pterm/putils"
)

// TreeBuilder provides a fluent API for constructing hierarchical displays
type TreeBuilder struct {
	leveledList pterm.LeveledList
	level       int
}

// TreeNode represents a single node in the tree with styling capabilities
type TreeNode struct {
	Text     string
	Level    int
	Icon     string
	Color    func(string) string
	Status   string
	Children []TreeNode
}

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

// NewTreeBuilder creates a new tree builder
func NewTreeBuilder() *TreeBuilder {
	return &TreeBuilder{
		leveledList: make(pterm.LeveledList, 0),
		level:       0,
	}
}

// NewClusterTreeBuilder creates a specialized cluster hierarchy builder
func NewClusterTreeBuilder() *ClusterTreeBuilder {
	return &ClusterTreeBuilder{
		builder: NewTreeBuilder(),
	}
}

// NewRegionTreeBuilder creates a specialized region organization builder
func NewRegionTreeBuilder() *RegionTreeBuilder {
	return &RegionTreeBuilder{
		builder: NewTreeBuilder(),
	}
}

// NewComparisonTreeBuilder creates a specialized comparison differences builder
func NewComparisonTreeBuilder() *ComparisonTreeBuilder {
	return &ComparisonTreeBuilder{
		builder: NewTreeBuilder(),
	}
}

// Tree-building methods for TreeBuilder

// AddRoot adds a root node to the tree
func (tb *TreeBuilder) AddRoot(text string) *TreeBuilder {
	tb.level = 0
	tb.leveledList = append(tb.leveledList, pterm.LeveledListItem{
		Level: 0,
		Text:  text,
	})
	return tb
}

// AddNode adds a node at the current level
func (tb *TreeBuilder) AddNode(text string) *TreeBuilder {
	tb.leveledList = append(tb.leveledList, pterm.LeveledListItem{
		Level: tb.level,
		Text:  text,
	})
	return tb
}

// AddChild adds a child node and increases the level
func (tb *TreeBuilder) AddChild(text string) *TreeBuilder {
	tb.level++
	tb.leveledList = append(tb.leveledList, pterm.LeveledListItem{
		Level: tb.level,
		Text:  text,
	})
	return tb
}

// AddSibling adds a sibling at the current level
func (tb *TreeBuilder) AddSibling(text string) *TreeBuilder {
	tb.leveledList = append(tb.leveledList, pterm.LeveledListItem{
		Level: tb.level,
		Text:  text,
	})
	return tb
}

// Up moves up one level in the hierarchy
func (tb *TreeBuilder) Up() *TreeBuilder {
	if tb.level > 0 {
		tb.level--
	}
	return tb
}

// UpTo moves up to a specific level
func (tb *TreeBuilder) UpTo(level int) *TreeBuilder {
	if level >= 0 && level < tb.level {
		tb.level = level
	}
	return tb
}

// AddNodeWithIcon adds a node with an icon and optional coloring
func (tb *TreeBuilder) AddNodeWithIcon(icon, text string, colorFunc func(string) string) *TreeBuilder {
	displayText := icon + " " + text
	if colorFunc != nil {
		displayText = colorFunc(displayText)
	}

	tb.leveledList = append(tb.leveledList, pterm.LeveledListItem{
		Level: tb.level,
		Text:  displayText,
	})
	return tb
}

// AddStatus adds a status-colored node
func (tb *TreeBuilder) AddStatus(icon, text, status string) *TreeBuilder {
	var colorFunc func(string) string
	var statusText string

	switch strings.ToUpper(status) {
	case "ACTIVE", "RUNNING", "HEALTHY", "PASS", "SUCCESS", "ENABLED":
		colorFunc = func(s string) string { return color.GreenString(s) }
		statusText = "PASS"
	case "UPDATING", "PENDING", "SCALING", "WARN", "WARNING":
		colorFunc = func(s string) string { return color.YellowString(s) }
		statusText = "WARN"
	case "FAILED", "ERROR", "CRITICAL", "FAIL", "DISABLED":
		colorFunc = func(s string) string { return color.RedString(s) }
		statusText = "FAIL"
	case "UNKNOWN", "NONE":
		colorFunc = func(s string) string { return color.WhiteString(s) }
		statusText = "UNKNOWN"
	default:
		colorFunc = func(s string) string { return color.WhiteString(s) }
		statusText = ""
	}

	var displayText string
	if statusText != "" && icon != "" {
		displayText = fmt.Sprintf("[%s] %s %s", statusText, icon, text)
	} else if statusText != "" {
		displayText = fmt.Sprintf("[%s] %s", statusText, text)
	} else if icon != "" {
		displayText = fmt.Sprintf("%s %s", icon, text)
	} else {
		displayText = text
	}

	// Apply color function (always set in switch above)
	displayText = colorFunc(displayText)

	tb.leveledList = append(tb.leveledList, pterm.LeveledListItem{
		Level: tb.level,
		Text:  displayText,
	})
	return tb
}

// Render outputs the tree to the console
func (tb *TreeBuilder) Render() error {
	if len(tb.leveledList) == 0 {
		return fmt.Errorf("no tree nodes to render")
	}

	// Convert to tree and render
	root := putils.TreeFromLeveledList(tb.leveledList)
	return pterm.DefaultTree.WithRoot(root).Render()
}

// RenderWithTitle outputs the tree with a section title
func (tb *TreeBuilder) RenderWithTitle(title string) error {
	pterm.DefaultSection.Println(title)
	return tb.Render()
}

// Specialized builders

// ClusterTreeBuilder methods

// AddCluster adds a cluster root node
func (ctb *ClusterTreeBuilder) AddCluster(name, status, version string, nodeCount int) *ClusterTreeBuilder {
	clusterText := fmt.Sprintf("%s (%s, %s, %d nodes)", name, status, version, nodeCount)
	ctb.builder.AddRoot("CLUSTER " + clusterText)
	return ctb
}

// AddNodegroup adds a nodegroup to the current cluster
func (ctb *ClusterTreeBuilder) AddNodegroup(name, status, instanceType string, readyNodes, desiredNodes int32) *ClusterTreeBuilder {
	ngText := fmt.Sprintf("%s (%s, %s, %d/%d nodes)", name, status, instanceType, readyNodes, desiredNodes)
	ctb.builder.AddChild("NODEGROUP " + ngText)
	return ctb
}

// AddInstance adds an instance to the current nodegroup
func (ctb *ClusterTreeBuilder) AddInstance(instanceId, state, az string) *ClusterTreeBuilder {
	instanceText := fmt.Sprintf("%s (%s, %s)", instanceId, state, az)
	ctb.builder.AddChild("INSTANCE " + instanceText)
	return ctb
}

// AddAddon adds an addon to the cluster
func (ctb *ClusterTreeBuilder) AddAddon(name, version, status string) *ClusterTreeBuilder {
	addonText := fmt.Sprintf("%s (%s)", name, version)
	ctb.builder.AddStatus("ADDON", addonText, status)
	return ctb
}

// FinishNodegroup moves back to cluster level
func (ctb *ClusterTreeBuilder) FinishNodegroup() *ClusterTreeBuilder {
	ctb.builder.UpTo(1)
	return ctb
}

// Render outputs the cluster tree
func (ctb *ClusterTreeBuilder) Render() error {
	return ctb.builder.Render()
}

// RenderWithTitle outputs the cluster tree with a title
func (ctb *ClusterTreeBuilder) RenderWithTitle(title string) error {
	return ctb.builder.RenderWithTitle(title)
}

// RegionTreeBuilder methods

// AddRegion adds a region root node
func (rtb *RegionTreeBuilder) AddRegion(name string, clusterCount int) *RegionTreeBuilder {
	regionText := fmt.Sprintf("%s (%d clusters)", name, clusterCount)
	rtb.builder.AddRoot("REGION " + regionText)
	return rtb
}

// AddClusterToRegion adds a cluster to the current region
func (rtb *RegionTreeBuilder) AddClusterToRegion(name, status string, nodeCount int32) *RegionTreeBuilder {
	clusterText := fmt.Sprintf("%s (%s, %d nodes)", name, status, nodeCount)
	rtb.builder.AddStatus("CLUSTER", clusterText, status)
	return rtb
}

// FinishRegion moves back to the root level for next region
func (rtb *RegionTreeBuilder) FinishRegion() *RegionTreeBuilder {
	rtb.builder.UpTo(0)
	return rtb
}

// Render outputs the region tree
func (rtb *RegionTreeBuilder) Render() error {
	return rtb.builder.Render()
}

// RenderWithTitle outputs the region tree with a title
func (rtb *RegionTreeBuilder) RenderWithTitle(title string) error {
	return rtb.builder.RenderWithTitle(title)
}

// ComparisonTreeBuilder methods

// AddComparisonRoot adds a comparison root node
func (ctb *ComparisonTreeBuilder) AddComparisonRoot(clusters []string) *ComparisonTreeBuilder {
	rootText := fmt.Sprintf("Cluster Comparison: %s", strings.Join(clusters, " vs "))
	ctb.builder.AddRoot("COMPARISON " + rootText)
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
	diffText := fmt.Sprintf("%s: %s", field, strings.Join(values, " vs "))
	ctb.builder.AddStatus("", diffText, severity)
	return ctb
}

// AddSimilarity adds a similarity (no difference)
func (ctb *ComparisonTreeBuilder) AddSimilarity(field, value string) *ComparisonTreeBuilder {
	simText := fmt.Sprintf("%s: %s (both)", field, value)
	ctb.builder.AddStatus("", simText, "SUCCESS")
	return ctb
}

// FinishCategory moves back to comparison root level
func (ctb *ComparisonTreeBuilder) FinishCategory() *ComparisonTreeBuilder {
	ctb.builder.UpTo(1)
	return ctb
}

// Render outputs the comparison tree
func (ctb *ComparisonTreeBuilder) Render() error {
	return ctb.builder.Render()
}

// RenderWithTitle outputs the comparison tree with a title
func (ctb *ComparisonTreeBuilder) RenderWithTitle(title string) error {
	return ctb.builder.RenderWithTitle(title)
}

// Utility functions

// FormatTreeSummary creates a summary line for tree displays
func FormatTreeSummary(itemCount int, itemType string, duration float64) string {
	return fmt.Sprintf("Found %d %s in %s",
		itemCount,
		itemType,
		color.GreenString("%.1fs", duration))
}

// TreePrefix returns appropriate text prefixes for different resource types
type TreePrefix struct{}

var Prefixes = TreePrefix{}

func (TreePrefix) Cluster() string   { return "CLUSTER" }
func (TreePrefix) Nodegroup() string { return "NODEGROUP" }
func (TreePrefix) Instance() string  { return "INSTANCE" }
func (TreePrefix) Region() string    { return "REGION" }
func (TreePrefix) World() string     { return "GLOBAL" }
func (TreePrefix) Addon() string     { return "ADDON" }
func (TreePrefix) Network() string   { return "NETWORK" }
func (TreePrefix) Security() string  { return "SECURITY" }
func (TreePrefix) Config() string    { return "CONFIG" }
func (TreePrefix) Compare() string   { return "COMPARISON" }
func (TreePrefix) Success() string   { return "PASS" }
func (TreePrefix) Warning() string   { return "WARN" }
func (TreePrefix) Error() string     { return "FAIL" }
func (TreePrefix) Unknown() string   { return "UNKNOWN" }
