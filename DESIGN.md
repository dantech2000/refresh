# refresh CLI Design Guidelines

## Overview

The refresh CLI follows specific design patterns for consistency, usability, and professional appearance. This document outlines the established conventions used across all commands including the new Enhanced Cluster Operations features.

## Visual Design Principles

### 1. No Emojis
- **NEVER** use emojis in command output
- Use text-based indicators and symbols instead
- Examples: `PASS`, `FAIL`, `[IN PROGRESS]`, `[SUCCESSFUL]`

### 2. Status Indicators
Use consistent text-based status indicators:
- `PASS` - Success/healthy state (green)
- `WARN` - Warning state (yellow) 
- `FAIL` - Error/failure state (red)
- `[IN PROGRESS]` - Operation in progress (cyan)
- `[SUCCESSFUL]` - Completed successfully (green)
- `[FAILED]` - Operation failed (red)

### 3. Progress Visualization

#### Progress Bars
Use ASCII progress bars with consistent format:
```
[████████████████████] Status Text    PASS
[██████████████████▒▒] Status Text    WARN  
[████████████████▒▒▒▒] Status Text    FAIL
```

#### Spinners
Use yacspin library with:
- CharSet 14: ⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏
- 100ms frequency
- Meaningful, contextual messages
- Humorous messages for long operations (see list.go example)

### 4. Color Scheme
Consistent color usage:
- **Green**: Success, healthy, positive states
- **Red**: Errors, failures, critical issues
- **Yellow**: Warnings, attention needed
- **Cyan**: In progress, informational
- **White**: Neutral, unknown states

### 5. Table Formatting
Use box-drawing characters for clean tables:
```
┌─────────────────┬─────────┬────────────┐
│ COLUMN HEADER   │ STATUS  │ VALUE      │
├─────────────────┼─────────┼────────────┤
│ data            │ PASS    │ value      │
└─────────────────┴─────────┴────────────┘
```

### 6. Legacy Table Formatting (DEPRECATED)

**DEPRECATED**: The old manual table formatting approach has been replaced by `ui.DynamicTable`:

```go
// OLD (PROBLEMATIC): Manual formatting with ANSI alignment issues
fmt.Printf("│ %-14s │ %s │ %-7s │ %s │\n",
    truncateString(cluster.Name, 14),
    padColoredString(formatStatus(cluster.Status), 7),  // DEPRECATED
    cluster.Version,
    padColoredString(healthStatus, 15))                 // DEPRECATED

// NEW (RECOMMENDED): Use DynamicTable for perfect alignment
table := ui.NewDynamicTable()
table.Add("Cluster", cluster.Name)
table.AddStatus("Status", cluster.Status)     // Auto-colored and aligned
table.Add("Version", cluster.Version)
table.AddStatus("Health", healthStatus)       // Auto-colored and aligned
table.Render()
```

### 7. Shared Table Renderer

Use `internal/ui/table.go` for all tabular CLI output. It provides:
- ANSI-aware width calculation, padding, and truncation
- Per-column `Min`/`Max` width with dynamic sizing
- Left/Right alignment (headers align with column alignment)
- Consistent borders and header coloring

Example:

```go
cols := []ui.Column{
  {Title: "NAME", Min: 4, Max: 24, Align: ui.AlignLeft},
  {Title: "READY/DESIRED", Min: 15, Max: 0, Align: ui.AlignRight},
}
tbl := ui.NewTable(cols, ui.WithHeaderColor(color.CyanString))
tbl.AddRow(name, fmt.Sprintf("%d/%d", ready, desired))
tbl.Render()
```

Avoid manual border construction or `fmt.Printf` alignment for tables in command code; use the shared renderer.

**Modern Approach with Dynamic Tables**:
```go
// RECOMMENDED: Use DynamicTable for automatic alignment
table := ui.NewDynamicTable()
table.Add("Cluster", clusterName)
table.AddStatus("Status", status)    // Automatic coloring and alignment
table.Render()

// ALTERNATIVE: Manual pterm table for complex layouts
tableData := pterm.TableData{
    {color.CyanString("CLUSTER"), color.CyanString("STATUS")},
    {clusterName, formatStatus(status)},
}
pterm.DefaultTable.WithHasHeader().WithData(tableData).Render()
```

## Command Structure

### 1. Flag Consistency
- Always provide short aliases: `-c, --cluster`
- Use consistent flag names across commands
- Environment variable support where appropriate
- Sensible defaults

### 2. Output Formats
Support multiple output formats:
- `table` (default): Human-readable tables
- `json`: Machine-readable JSON
- `yaml`: Configuration-friendly YAML

### 3. Error Handling
- Use color.Red() for error messages
- Provide actionable error messages
- Include help text for common issues
- Early credential validation

### 4. Performance Indicators
Show performance in a friendly way:
```
Retrieved in 1.2s
```

## Health Check Display Patterns

### Status Display Format
```
Cluster Health Assessment:

[████████████████████] Node Health          PASS
[████████████████████] Cluster Capacity     PASS  
[████████████████████] Critical Workloads   PASS
[██████████████████▒▒] Pod Disruption Budgets WARN
[█████████████████▒▒▒] Resource Balance     WARN

Status: READY WITH WARNINGS (2 issues found)
```

### Decision Text
- `READY FOR UPDATE` (green)
- `READY WITH WARNINGS` (yellow)
- `CRITICAL ISSUES FOUND` (red)

## Spinner and Loading States

### Messages
- Professional but can include light humor
- Contextual to the operation
- Rotate messages for long operations
- Clear completion messages

### Example Patterns
```go
// Phase 2: PTerm-based progress indicators
spinner := ui.NewProgressSpinner("Gathering cluster information...")
cancelSpinner := spinner.Start(ctx)
defer cancelSpinner()
// ... operation
spinner.Success("Operation completed!")
```

## User Interaction

### Prompts
Use clear, actionable prompts:
```
Proceed with update? (Y/n): 
```

### Confirmation
Show what will happen before destructive operations:
```
Multiple clusters match pattern 'prod':
  1) prod-api
  2) prod-workers
Select cluster number (1-2) or press Enter to cancel:
```

## Performance Indicators

### Always Show Timing
```go
startTime := time.Now()
// ... operation
elapsed := time.Since(startTime)
fmt.Printf("Retrieved in %s\n", color.GreenString(elapsed.String()))
```

### Comparison with Alternatives
Keep performance messaging neutral and data-driven; avoid competitor references.

## Text Formatting

### Headers and Sections
```
Cluster Information: cluster-name
EKS Clusters (3 regions, 8 clusters)
Comparison Summary:
```

### Status Text
- UPPERCASE for status values: `ACTIVE`, `PASS`, `FAIL`
- Consistent spacing and alignment
- Clear visual hierarchy

### Error Messages and Warnings
- Concise, actionable messages
- Avoid technical jargon where possible
- Include next steps when appropriate

## Examples to Follow

### Good Status Display
```
Status: READY WITH WARNINGS (2 issues found)
```

### Good Table Header
```
┌────────────────┬─────────┬─────────┬──────────┐
│ CLUSTER        │ STATUS  │ VERSION │ HEALTH   │
├────────────────┼─────────┼─────────┼──────────┤
│ prod-api       │ ACTIVE  │ 1.30    │ PASS     │
└────────────────┴─────────┴─────────┴──────────┘
```

### Good Progress Indication
```
Retrieved in 1.2s
```

## Anti-Patterns (Avoid)

### Don't Use
- Emojis: ❌ ✅ ⚠️ 🚀 🎯
- Inconsistent colors
- Unclear status text
- Missing timing information
- Overly technical error messages

### Don't Format Like This
```
// BAD - uses emojis and inconsistent formatting
Status: ✅ Everything looks good! 🎉
```

### Instead Use
```
// GOOD - consistent text-based formatting
Status: READY FOR UPDATE
```

## Dynamic Table Pattern (Recommended)

### 1. Use Dynamic Tables for Key-Value Displays

**ALWAYS** use `ui.DynamicTable` for key-value displays to ensure perfect alignment regardless of content length:

```go
// GOOD - Dynamic table with automatic alignment
table := ui.NewDynamicTable()
table.Add("Status", formatStatus(details.Status))
table.Add("Deletion Protection", deletionProtectionStatus)  // Auto-aligns!
table.Render()

// BAD - Fixed-width formatting breaks with longer keys
printTableRow("Status", formatStatus(details.Status))
printTableRow("Deletion Protection", deletionProtectionStatus)  // Misaligned!
```

### 2. Dynamic Table Benefits

- **🎯 Perfect Alignment**: Automatically calculates optimal column width
- **🌈 ANSI-Aware**: Handles colored text without breaking alignment
- **🔄 Future-Proof**: Adding new fields never breaks formatting
- **🧹 Clean Code**: Chainable API for readability

### 3. Dynamic Table API

#### Basic Usage
```go
table := ui.NewDynamicTable()
table.Add("Key", "Value")
table.Render()
```

#### Chained Operations
```go
table := ui.NewDynamicTable().
    Add("Status", "Active").
    AddStatus("Health", "ENABLED").     // Auto-colored status
    AddBool("Protection", true).        // Auto ENABLED/DISABLED
    AddIf(hasNodes, "Nodes", nodeCount) // Conditional rows
```

#### Section Rendering
```go
table.RenderSection("Cluster Information")  // With header
```

### 4. Status Coloring Patterns

Use built-in status methods for consistent coloring:

```go
// Automatic color coding
table.AddStatus("Health", "ACTIVE")    // → Green
table.AddStatus("Status", "FAILED")    // → Red  
table.AddStatus("State", "WARN")       // → Yellow
table.AddBool("Enabled", true)         // → Green "ENABLED"
```

### 5. Custom Coloring
```go
table.AddColored("Custom", "value", color.CyanString)
```

### 6. Migration from Fixed-Width

**Before (Problematic)**:
```go
printTableRow("Status", formatStatus(details.Status))
printTableRow("Very Long Field Name", value)  // BREAKS ALIGNMENT!
```

**After (Perfect)**:
```go
ui.NewDynamicTable().
    Add("Status", formatStatus(details.Status)).
    Add("Very Long Field Name", value).         // PERFECT ALIGNMENT!
    Render()
```

### 7. Testing Alignment

All dynamic table usage is automatically tested for alignment correctness via `dynamic_table_test.go`.

## PTerm Implementation Progress

### ✅ **Phase 1: Table Rendering (COMPLETED)**

**Status**: **COMPLETE** - Successfully implemented and deployed

**What was accomplished**:
- ✅ **Dynamic Table System**: Created `internal/ui/dynamic_table.go` with automatic width calculation
- ✅ **ANSI-Aware Alignment**: Perfect table alignment regardless of colored text content  
- ✅ **Rich API**: Chainable methods with built-in status coloring (`AddStatus`, `AddBool`, `AddIf`)
- ✅ **Comprehensive Testing**: Full test suite with alignment validation
- ✅ **Commands Migrated**: Updated `describe_cluster`, `describe_nodegroup`, `compare_clusters`
- ✅ **Documentation**: Complete API reference and migration guide
- ✅ **Legacy Cleanup**: Removed obsolete `padColoredString` and `stripAnsiCodes` functions

**Key Benefits Achieved**:
- 🎯 **Zero Formatting Issues**: Never breaks alignment when adding new fields
- 🧹 **Simplified Code**: Clean, maintainable table rendering patterns
- 📱 **Future-Proof**: Automatic width calculation handles any content

### ✅ **Phase 2: Progress Indicators (COMPLETED)**

**Status**: **COMPLETE** - Successfully implemented and deployed

**What was accomplished**:
- ✅ **Rich Progress System**: Created `internal/ui/progress.go` with comprehensive pterm-based progress utilities
- ✅ **Spinner Migration**: Replaced all pin and yacspin usage with pterm's advanced spinners
- ✅ **Multi-Region Progress**: Enhanced `list_clusters.go` with `RegionProgressTracker` for concurrent region queries
- ✅ **Consistent API**: Unified progress indicator patterns across all commands
- ✅ **Performance Integration**: Built-in timing displays with `PerformanceTimer`
- ✅ **Multi-Printer Support**: Concurrent progress bars and spinners via `MultiProgressManager`
- ✅ **Dependency Cleanup**: Removed yacspin and pin dependencies from project

**Key Benefits Achieved**:
- 🎯 **Enhanced UX**: Rich visual feedback for multi-region operations
- 🎨 **Consistent Styling**: All progress indicators follow design system colors (cyan/yellow)
- ⚡ **Performance Visibility**: Real-time progress tracking across concurrent operations
- 🧹 **Simplified Code**: Clean, maintainable progress patterns across all commands

**Files Updated**:
- ✅ `internal/ui/progress.go` - New comprehensive progress utilities
- ✅ `internal/ui/health.go` - Migrated to pterm-based health spinner
- ✅ `internal/commands/list_clusters.go` - Enhanced with multi-region progress tracking
- ✅ `internal/commands/describe_cluster.go` - Migrated to pterm spinner
- ✅ `internal/commands/describe_nodegroup.go` - Migrated to pterm spinner
- ✅ `internal/commands/list_nodegroups.go` - Migrated to pterm spinner
- ✅ `internal/commands/compare_clusters.go` - Migrated to pterm spinner
- ✅ `internal/commands/nodegroup_recommendations.go` - Migrated to pterm spinner
- ✅ `internal/commands/list_addons.go` - Migrated to pterm spinner
- ✅ `internal/commands/scale_nodegroup.go` - Enhanced with Success/Fail states
- ✅ `internal/commands/list.go` - Migrated with dynamic message updating
- ✅ `internal/commands/update.go` - Updated health spinner usage
- ✅ `go.mod` - Removed yacspin and pin dependencies

### ✅ **Phase 3: Tree Visualization (COMPLETED)**

**Status**: **COMPLETE** - Successfully implemented and deployed

**What was accomplished**:
- ✅ **Comprehensive Tree System**: Created `internal/ui/tree.go` with fluent tree-building API
- ✅ **Multi-Region Organization**: Enhanced `list-clusters` with `--tree` flag for hierarchical region/cluster display
- ✅ **Cluster Hierarchies**: Built specialized builders for cluster → nodegroups → instances visualization
- ✅ **Comparison Trees**: Created comparison tree builder for hierarchical difference analysis
- ✅ **Status Integration**: Text-based status indicators with color coding (`[PASS]`, `[WARN]`, `[FAIL]`)
- ✅ **Design Compliance**: Completely emoji-free with text-based prefixes following design guidelines
- ✅ **Flexible API**: Chainable tree builders with specialized components for different use cases

**Key Benefits Achieved**:
- 🎯 **Enhanced Visualization**: Hierarchical display shows relationships clearly
- 🌍 **Multi-Region Clarity**: Tree view organizes clusters by region for better overview
- 🔍 **Detailed Drill-down**: Support for cluster → nodegroup → instance hierarchies
- 🎨 **Professional Appearance**: Clean, text-based formatting following design system
- 🧹 **Maintainable Code**: Fluent API makes tree construction simple and readable

**Files Created/Updated**:
- ✅ `internal/ui/tree.go` - Complete tree visualization system
- ✅ `internal/commands/list_clusters.go` - Added `--tree` and `--format tree` options
- ✅ Tree builders: `ClusterTreeBuilder`, `RegionTreeBuilder`, `ComparisonTreeBuilder`
- ✅ Status formatting: `[PASS]`, `[WARN]`, `[FAIL]` text-based indicators
- ✅ Text prefixes: `CLUSTER`, `NODEGROUP`, `INSTANCE`, `REGION`, etc.

**Usage Examples**:
```bash
# Multi-region tree view
refresh cluster list --tree --all-regions

# Tree format output
refresh cluster list --format tree

# Tree view with health status
refresh cluster list --tree --show-health
```

### 🎮 **Phase 4: Interactive Elements (PLANNED)**

**Status**: **FUTURE** - Research phase

**Scope**: Optional interactive features for complex workflows

**Planned Features**:
- 📋 **Multi-Select**: Interactive cluster/nodegroup selection
- 🔍 **Live Filtering**: Real-time search and filtering
- 📊 **Interactive Tables**: Sortable columns, expandable rows
- 🎯 **Confirmation Dialogs**: Enhanced safety for destructive operations

**Design Constraints**:
- ✅ **Non-Breaking**: All features remain scriptable
- ✅ **Fallback Mode**: Graceful degradation for CI/CD environments
- ✅ **Performance**: No impact on current fast operation speeds

### 🛠️ **Implementation Guidelines**

**For Future Phases**:

1. **Maintain Compatibility**: All current commands and flags must work unchanged
2. **Progressive Enhancement**: New features should enhance, not replace existing functionality  
3. **Performance First**: No feature should slow down current operations
4. **Testing Required**: All new UI components need comprehensive test coverage
5. **Documentation**: Update both user and developer documentation

**Migration Pattern**:
```go
// Current (Phase 1)
table := ui.NewDynamicTable().Add(key, value).Render()

// Future (Phase 2) 
progress := ui.NewProgressBar().WithETA().Start()
// ... operation
progress.Finish("Operation completed")

// Future (Phase 3)
tree := ui.NewTree().AddNode(parent, child).Render()
```

This design system ensures professional, consistent, and accessible output across all refresh CLI commands.