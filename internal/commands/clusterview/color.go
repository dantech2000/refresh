package clusterview

import (
	"fmt"
	"strings"
	"time"

	"github.com/dantech2000/refresh/internal/health"
	clustersvc "github.com/dantech2000/refresh/internal/services/cluster"
)

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

// decisionLabel is the upper-case word the human and plain views show for
// a health decision (PROCEED, WARN, BLOCK). The JSON value is PascalCase.
func decisionLabel(d health.Decision) string { return strings.ToUpper(string(d)) }

// healthStatusLabel is the upper-case word the human and plain views show
// for a health check status (PASS, WARN, FAIL).
func healthStatusLabel(s health.HealthStatus) string { return strings.ToUpper(string(s)) }

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

// nodeCountText renders a NODES cell honestly: a measured "ready/desired"
// fraction only when readiness was actually measured, otherwise just the
// desired count — never a fabricated ready figure. (REF-130)
func nodeCountText(readyKnown bool, ready, desired int32) string {
	if readyKnown {
		return fmt.Sprintf("%d/%d", ready, desired)
	}
	return fmt.Sprintf("%d", desired)
}

// nodeCountInfoText applies nodeCountText to an aggregated NodeCountInfo.
func nodeCountInfoText(n clustersvc.NodeCountInfo) string {
	return nodeCountText(n.ReadyKnown, n.Ready, n.Total)
}

func truncateEndpoint(endpoint string) string {
	if len(endpoint) > 120 {
		return endpoint[:117] + "..."
	}
	return endpoint
}

func formatAge(d time.Duration) string {
	// Clamp negatives (clock skew / a future timestamp) so we never render a
	// nonsensical "-3 minutes".
	if d < 0 {
		d = 0
	}
	if days := int(d.Hours() / 24); days > 0 {
		return fmt.Sprintf("%d days", days)
	}
	if hours := int(d.Hours()); hours > 0 {
		return fmt.Sprintf("%d hours", hours)
	}
	if mins := int(d.Minutes()); mins > 0 {
		return fmt.Sprintf("%d minutes", mins)
	}
	return "just now"
}
