package types

import "github.com/fatih/color"

// AMIStatus represents the status of a nodegroup's AMI relative to the latest available.
type AMIStatus int

const (
	// AMILatest indicates the nodegroup is using the latest AMI.
	AMILatest AMIStatus = iota
	// AMIOutdated indicates the nodegroup is using an older AMI.
	AMIOutdated
	// AMIUpdating indicates the nodegroup is currently being updated.
	AMIUpdating
	// AMIUnknown indicates the AMI status could not be determined.
	AMIUnknown
)

// String returns a human-readable, color-coded string representation of the AMI status.
func (s AMIStatus) String() string {
	switch s {
	case AMILatest:
		return color.GreenString("Latest")
	case AMIOutdated:
		return color.RedString("Outdated")
	case AMIUpdating:
		return color.YellowString("Updating")
	default:
		return color.WhiteString("Unknown")
	}
}

// PlainString returns a plain string representation without color codes.
func (s AMIStatus) PlainString() string {
	switch s {
	case AMILatest:
		return "Latest"
	case AMIOutdated:
		return "Outdated"
	case AMIUpdating:
		return "Updating"
	default:
		return "Unknown"
	}
}

// NeedsUpdate returns true if the nodegroup should be updated.
func (s AMIStatus) NeedsUpdate() bool {
	return s == AMIOutdated
}

// DryRunAction represents the action that would be taken in dry run mode.
type DryRunAction int

const (
	// ActionUpdate indicates the nodegroup will be updated.
	ActionUpdate DryRunAction = iota
	// ActionSkipUpdating indicates the nodegroup is already updating.
	ActionSkipUpdating
	// ActionSkipLatest indicates the nodegroup is already at the latest AMI.
	ActionSkipLatest
	// ActionForceUpdate indicates the nodegroup will be force-updated.
	ActionForceUpdate
)

// String returns a human-readable, color-coded string representation of the action.
func (a DryRunAction) String() string {
	switch a {
	case ActionUpdate:
		return color.GreenString("UPDATE")
	case ActionSkipUpdating:
		return color.YellowString("SKIP")
	case ActionSkipLatest:
		return color.GreenString("SKIP")
	case ActionForceUpdate:
		return color.CyanString("FORCE UPDATE")
	default:
		return color.WhiteString("UNKNOWN")
	}
}

// PlainString returns a plain string representation without color codes.
func (a DryRunAction) PlainString() string {
	switch a {
	case ActionUpdate:
		return "UPDATE"
	case ActionSkipUpdating:
		return "SKIP (already updating)"
	case ActionSkipLatest:
		return "SKIP (already latest)"
	case ActionForceUpdate:
		return "FORCE UPDATE"
	default:
		return "UNKNOWN"
	}
}

// Reason returns a human-readable reason for the action.
func (a DryRunAction) Reason() string {
	switch a {
	case ActionUpdate:
		return "AMI is outdated"
	case ActionSkipUpdating:
		return "Update already in progress"
	case ActionSkipLatest:
		return "Already using latest AMI"
	case ActionForceUpdate:
		return "Force flag specified"
	default:
		return "Unknown reason"
	}
}
