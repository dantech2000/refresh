package types

import (
	"encoding/json"

	"github.com/fatih/color"
)

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
	// AMICustom indicates the nodegroup runs a custom AMI (AmiType=CUSTOM) whose
	// AMI is managed via the user's launch template, not by EKS. refresh can't
	// pick a recommended AMI for these, so they are neither "latest" nor "stale".
	AMICustom
)

// String returns the plain, uncolored representation. Presentation (color)
// lives in ColorString so that %v formatting, logs, and serialization never
// emit ANSI escape codes.
func (s AMIStatus) String() string {
	return s.PlainString()
}

// ColorString returns a color-coded representation for terminal display.
func (s AMIStatus) ColorString() string {
	switch s {
	case AMILatest:
		return color.GreenString("Latest")
	case AMIOutdated:
		return color.RedString("Outdated")
	case AMIUpdating:
		return color.YellowString("Updating")
	case AMICustom:
		return color.CyanString("Custom")
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
	case AMICustom:
		return "Custom"
	default:
		return "Unknown"
	}
}

// MarshalJSON emits the plain string ("Latest", "Outdated", ...) instead of a
// bare enum int, so `-o json` consumers get a meaningful value.
func (s AMIStatus) MarshalJSON() ([]byte, error) {
	return json.Marshal(s.PlainString())
}

// UnmarshalJSON accepts the plain-string form produced by MarshalJSON (and
// the legacy integer form).
func (s *AMIStatus) UnmarshalJSON(data []byte) error {
	var str string
	if err := json.Unmarshal(data, &str); err == nil {
		switch str {
		case "Latest":
			*s = AMILatest
		case "Outdated":
			*s = AMIOutdated
		case "Updating":
			*s = AMIUpdating
		case "Custom":
			*s = AMICustom
		default:
			*s = AMIUnknown
		}
		return nil
	}
	var n int
	if err := json.Unmarshal(data, &n); err != nil {
		return err
	}
	*s = AMIStatus(n)
	return nil
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

// ColorString returns a color-coded representation for terminal display.
func (a DryRunAction) ColorString() string {
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
