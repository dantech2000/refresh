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

### 5. Table Alignment with Colored Text

**CRITICAL**: When using colored text in tables, proper alignment requires special handling:

```go
// CORRECT: Use padColoredString for colored text in table cells
fmt.Printf("│ %-14s │ %s │ %-7s │ %s │\n",
    truncateString(cluster.Name, 14),
    padColoredString(formatStatus(cluster.Status), 7),
    cluster.Version,
    padColoredString(healthStatus, 15))

// WRONG: Direct printf formatting with colored text breaks alignment
fmt.Printf("│ %-14s │ %-7s │ %-7s │ %-15s │\n",
    truncateString(cluster.Name, 14),
    formatStatus(cluster.Status),  // This contains ANSI codes!
    cluster.Version,
    healthStatus)                  // This also contains ANSI codes!
```

**Key Functions**:
- `padColoredString(s string, width int)`: Pads colored strings to exact width
- `stripAnsiCodes(s string)`: Removes ANSI escape codes for length calculation
- Always use these functions for colored text in table formatting

**Headers and Data Consistency**:
```go
// Both headers and data rows must use consistent padding
// Header
fmt.Printf("│ %s │ %s │\n",
    padColoredString(color.CyanString("CLUSTER"), 14),
    padColoredString(color.CyanString("STATUS"), 7))

// Data row  
fmt.Printf("│ %s │ %s │\n",
    padColoredString(clusterName, 14),
    padColoredString(formatStatus(status), 7))
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
spinner := pin.New("Gathering cluster information...",
    pin.WithSpinnerColor(pin.ColorCyan),
    pin.WithTextColor(pin.ColorYellow),
)
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

This design system ensures professional, consistent, and accessible output across all refresh CLI commands.