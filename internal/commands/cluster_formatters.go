package commands

import (
	"fmt"
	"strings"
	"time"

	"github.com/fatih/color"

	"github.com/dantech2000/refresh/internal/health"
)

func formatStatus(status string) string {
	switch strings.ToUpper(status) {
	case "ACTIVE":
		return color.GreenString("Active")
	case "CREATING":
		return color.YellowString("Creating")
	case "UPDATING":
		return color.YellowString("Updating")
	case "DELETING":
		return color.RedString("Deleting")
	case "FAILED":
		return color.RedString("Failed")
	default:
		return status
	}
}

func formatHealth(h *health.HealthSummary) string {
	if h == nil {
		return color.WhiteString("UNKNOWN")
	}
	passed := 0
	for _, r := range h.Results {
		if r.Status == health.StatusPass {
			passed++
		}
	}
	total := len(h.Results)
	switch h.Decision {
	case health.DecisionProceed:
		return color.GreenString("PASS (%d/%d checks passed)", passed, total)
	case health.DecisionWarn:
		return color.YellowString("WARN (%d issues)", len(h.Warnings)+len(h.Errors))
	case health.DecisionBlock:
		return color.RedString("FAIL (%d issues)", len(h.Errors))
	default:
		return color.WhiteString("UNKNOWN")
	}
}

func formatAddonHealth(h string) string {
	switch h {
	case "Healthy":
		return color.GreenString("PASS")
	case "Issues", "Failed":
		return color.RedString("FAIL")
	case "Updating":
		return color.CyanString("[IN PROGRESS]")
	default:
		return color.WhiteString("UNKNOWN")
	}
}

func truncateEndpoint(endpoint string) string {
	if len(endpoint) > 120 {
		return endpoint[:117] + "..."
	}
	return endpoint
}

func formatAge(d time.Duration) string {
	if days := int(d.Hours() / 24); days > 0 {
		return fmt.Sprintf("%d days", days)
	}
	if hours := int(d.Hours()); hours > 0 {
		return fmt.Sprintf("%d hours", hours)
	}
	return fmt.Sprintf("%d minutes", int(d.Minutes()))
}
