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

// NewTreeBuilder creates a new tree builder with pre-allocated capacity
func NewTreeBuilder() *TreeBuilder {
	return &TreeBuilder{
		leveledList: make(pterm.LeveledList, 0, 16),
	}
}

// AddRoot adds a root node to the tree
func (tb *TreeBuilder) AddRoot(text string) *TreeBuilder {
	tb.level = 0
	tb.leveledList = append(tb.leveledList, pterm.LeveledListItem{Level: 0, Text: text})
	return tb
}

// AddNode adds a node at the current level
func (tb *TreeBuilder) AddNode(text string) *TreeBuilder {
	tb.leveledList = append(tb.leveledList, pterm.LeveledListItem{Level: tb.level, Text: text})
	return tb
}

// AddChild adds a child node and increases the level
func (tb *TreeBuilder) AddChild(text string) *TreeBuilder {
	tb.level++
	tb.leveledList = append(tb.leveledList, pterm.LeveledListItem{Level: tb.level, Text: text})
	return tb
}

// AddSibling adds a sibling at the current level
func (tb *TreeBuilder) AddSibling(text string) *TreeBuilder {
	tb.leveledList = append(tb.leveledList, pterm.LeveledListItem{Level: tb.level, Text: text})
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
	tb.leveledList = append(tb.leveledList, pterm.LeveledListItem{Level: tb.level, Text: displayText})
	return tb
}

// AddStatus adds a status-colored node
func (tb *TreeBuilder) AddStatus(icon, text, status string) *TreeBuilder {
	var colorFunc func(string) string
	var statusText string

	switch strings.ToUpper(status) {
	case "ACTIVE", "RUNNING", "HEALTHY", "PASS", "SUCCESS", "ENABLED":
		colorFunc = func(s string) string { return color.GreenString("%s", s) }
		statusText = "PASS"
	case "UPDATING", "PENDING", "SCALING", "WARN", "WARNING":
		colorFunc = func(s string) string { return color.YellowString("%s", s) }
		statusText = "WARN"
	case "FAILED", "ERROR", "CRITICAL", "FAIL", "DISABLED":
		colorFunc = func(s string) string { return color.RedString("%s", s) }
		statusText = "FAIL"
	default:
		colorFunc = func(s string) string { return color.WhiteString("%s", s) }
		statusText = ""
	}

	var displayText string
	switch {
	case statusText != "" && icon != "":
		displayText = fmt.Sprintf("[%s] %s %s", statusText, icon, text)
	case statusText != "":
		displayText = fmt.Sprintf("[%s] %s", statusText, text)
	case icon != "":
		displayText = fmt.Sprintf("%s %s", icon, text)
	default:
		displayText = text
	}

	tb.leveledList = append(tb.leveledList, pterm.LeveledListItem{
		Level: tb.level,
		Text:  colorFunc(displayText),
	})
	return tb
}

// Render outputs the tree to the console
func (tb *TreeBuilder) Render() error {
	if len(tb.leveledList) == 0 {
		return fmt.Errorf("no tree nodes to render")
	}
	root := putils.TreeFromLeveledList(tb.leveledList)
	return pterm.DefaultTree.WithRoot(root).Render()
}

// RenderWithTitle outputs the tree with a section title
func (tb *TreeBuilder) RenderWithTitle(title string) error {
	pterm.DefaultSection.Println(title)
	return tb.Render()
}

// FormatTreeSummary creates a summary line for tree displays
func FormatTreeSummary(itemCount int, itemType string, duration float64) string {
	return fmt.Sprintf("Found %d %s in %s", itemCount, itemType, color.GreenString("%.1fs", duration))
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
