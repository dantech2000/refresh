package ui

import (
	"github.com/aws/aws-sdk-go-v2/service/eks/types"
	"github.com/fatih/color"
)

// GetStatusPrefix returns an appropriate text prefix for the update status
func GetStatusPrefix(status types.UpdateStatus) string {
	switch status {
	case types.UpdateStatusInProgress:
		return "[IN PROGRESS]"
	case types.UpdateStatusSuccessful:
		return "[SUCCESSFUL]"
	case types.UpdateStatusFailed:
		return "[FAILED]"
	case types.UpdateStatusCancelled:
		return "[CANCELLED]"
	default:
		return "[UNKNOWN]"
	}
}

// GetStatusColor returns a color function for the update status
func GetStatusColor(status types.UpdateStatus) func(format string, a ...interface{}) string {
	switch status {
	case types.UpdateStatusInProgress:
		return color.CyanString
	case types.UpdateStatusSuccessful:
		return color.GreenString
	case types.UpdateStatusFailed:
		return color.RedString
	case types.UpdateStatusCancelled:
		return color.YellowString
	default:
		return color.WhiteString
	}
}
