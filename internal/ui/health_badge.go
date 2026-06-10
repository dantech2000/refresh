package ui

import "github.com/fatih/color"

// Health badges shared by every command that renders a HEALTH column, so the
// colored vocabulary (PASS / FAIL / [IN PROGRESS] / UNKNOWN) cannot drift
// between commands.

// BadgePass renders the green passing-health badge.
func BadgePass() string { return color.GreenString("PASS") }

// BadgeFail renders the red failing-health badge.
func BadgeFail() string { return color.RedString("FAIL") }

// BadgeInProgress renders the cyan in-progress badge.
func BadgeInProgress() string { return color.CyanString("[IN PROGRESS]") }

// BadgeUnknown renders the white unknown-health badge.
func BadgeUnknown() string { return color.WhiteString("UNKNOWN") }
