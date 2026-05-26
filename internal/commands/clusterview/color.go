package clusterview

import (
	"fmt"
	"strings"
	"time"

	"github.com/fatih/color"

	"github.com/dantech2000/refresh/internal/health"
	clustersvc "github.com/dantech2000/refresh/internal/services/cluster"
)

// colorString is the signature of fatih/color's *String helpers.
type colorString = func(format string, a ...interface{}) string

func severityColor(severity string) colorString {
	switch severity {
	case "critical":
		return color.RedString
	case "warning":
		return color.YellowString
	case "info":
		return color.BlueString
	default:
		return fmt.Sprintf
	}
}

func decisionColor(d health.Decision) colorString {
	switch d {
	case health.DecisionProceed:
		return color.GreenString
	case health.DecisionWarn:
		return color.YellowString
	case health.DecisionBlock:
		return color.RedString
	default:
		return color.WhiteString
	}
}

func severityHeading(sev string) string {
	switch sev {
	case "critical":
		return "Critical Issues"
	case "warning":
		return "Warnings"
	case "info":
		return "Information"
	default:
		return sev
	}
}

func formatDifferenceCount(count int, severity string) string {
	if count == 0 {
		return "0"
	}
	return severityColor(severity)("%d", count)
}

var statusStyles = map[string]struct {
	label string
	c     colorString
}{
	"ACTIVE":   {"Active", color.GreenString},
	"CREATING": {"Creating", color.YellowString},
	"UPDATING": {"Updating", color.YellowString},
	"DELETING": {"Deleting", color.RedString},
	"FAILED":   {"Failed", color.RedString},
}

func formatStatus(status string) string {
	if s, ok := statusStyles[strings.ToUpper(status)]; ok {
		return s.c(s.label)
	}
	return status
}

// healthLabel returns the short PASS/WARN/FAIL/UNKNOWN label for a decision.
func healthLabel(d health.Decision) string {
	switch d {
	case health.DecisionProceed:
		return "PASS"
	case health.DecisionWarn:
		return "WARN"
	case health.DecisionBlock:
		return "FAIL"
	default:
		return "UNKNOWN"
	}
}

// knownHealthTreeLabel returns the long HEALTHY/WARNING/CRITICAL label the
// tree view uses for a recognized decision, plus ok=true. For an
// empty/unrecognized decision it returns ("", false) so the caller can fall
// back to the underlying cluster status instead of masking it as "UNKNOWN".
func knownHealthTreeLabel(d health.Decision) (string, bool) {
	switch d {
	case health.DecisionProceed:
		return "HEALTHY", true
	case health.DecisionWarn:
		return "WARNING", true
	case health.DecisionBlock:
		return "CRITICAL", true
	default:
		return "", false
	}
}

// treeStatusWithHealth produces the status cell shown in the tree view.
// Known decisions replace the status with the health label; unknown decisions
// preserve the cluster status and append a "(health unknown)" hint so the
// operator can still tell the cluster's underlying state.
func treeStatusWithHealth(clusterStatus string, h *health.HealthSummary) string {
	if h == nil {
		return clusterStatus
	}
	if label, ok := knownHealthTreeLabel(h.Decision); ok {
		return label
	}
	return clusterStatus + " (health unknown)"
}

func formatHealth(h *health.HealthSummary) string {
	if h == nil {
		return color.WhiteString("UNKNOWN")
	}
	c := decisionColor(h.Decision)
	switch h.Decision {
	case health.DecisionProceed:
		passed := 0
		for _, r := range h.Results {
			if r.Status == health.StatusPass {
				passed++
			}
		}
		return c("PASS (%d/%d checks passed)", passed, len(h.Results))
	case health.DecisionWarn:
		return c("WARN (%d issues)", len(h.Warnings)+len(h.Errors))
	case health.DecisionBlock:
		return c("FAIL (%d issues)", len(h.Errors))
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

func formatClusterHealth(h *health.HealthSummary) string {
	if h == nil {
		return color.WhiteString("UNKNOWN")
	}
	return decisionColor(h.Decision)(healthLabel(h.Decision))
}

func formatNodeCount(n clustersvc.NodeCountInfo) string {
	switch {
	case n.Total == 0:
		return "0/0 ready"
	case n.Ready == n.Total:
		return color.GreenString("%d/%d ready", n.Ready, n.Total)
	case n.Ready == 0:
		return color.RedString("%d/%d ready", n.Ready, n.Total)
	default:
		return color.YellowString("%d/%d ready", n.Ready, n.Total)
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

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}
