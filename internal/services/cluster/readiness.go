package cluster

import (
	"fmt"
	"strings"

	"github.com/dantech2000/refresh/internal/health"
)

// Readiness is the upgrade-readiness verdict of an UpgradeReport. The
// `cluster upgrade-check` table shows it, and the command's exit code
// follows it (REF-165).
type Readiness int

const (
	// ReadinessReady: no finding.
	ReadinessReady Readiness = iota
	// ReadinessReview: warnings only (exit 2).
	ReadinessReview
	// ReadinessIncomplete: nothing blocks, but some skew data could not be
	// read, so the verdict does not cover everything (exit 4).
	ReadinessIncomplete
	// ReadinessBlocked: something blocks the upgrade (exit 3).
	ReadinessBlocked
)

// Readiness collapses the report into one verdict and the reasons for it,
// blockers first. Blocked: an ERROR or UNKNOWN insight (UNKNOWN blocks
// because the readiness gate in `cluster upgrade` refuses a hop on it), a
// nodegroup at the kubelet skew limit, or a failed control-plane health
// check. Review: a WARNING insight, a nodegroup behind the control plane, an
// addon behind latest, or a control-plane health warning. Incomplete: some
// nodegroups or add-ons could not be read (r.Failures). Precedence:
// blocked, then incomplete, then review, then ready. A known blocker wins
// over missing data; missing data wins over warnings, because the unread
// item could be a blocker.
func (r *UpgradeReport) Readiness() (Readiness, []string) {
	if r == nil {
		return ReadinessReady, nil
	}
	var errs, unknown, warn int
	for _, in := range r.Insights {
		switch strings.ToUpper(in.Status) {
		case InsightStatusPassing:
		case InsightStatusWarning:
			warn++
		case InsightStatusError:
			errs++
		default: // UNKNOWN, or a status this build doesn't know
			unknown++
		}
	}
	var skewBlocking, skewBehind, addonsBehind int
	for _, ng := range r.Skew.Nodegroups {
		switch {
		case ng.Blocking:
			skewBlocking++
		case ng.MinorsBehind > 0:
			skewBehind++
		}
	}
	for _, a := range r.Skew.Addons {
		if a.Behind {
			addonsBehind++
		}
	}
	cpFail, cpWarn := false, false
	if cp := r.ControlPlane; cp != nil && !cp.Skipped {
		cpFail = cp.Status == health.StatusFail
		cpWarn = cp.Status == health.StatusWarn
	}

	var blockers, warnings []string
	add := func(list *[]string, n int, format string) {
		if n > 0 {
			*list = append(*list, fmt.Sprintf(format, n))
		}
	}
	add(&blockers, errs, "%d ERROR insight(s)")
	add(&blockers, unknown, "%d UNKNOWN insight(s)")
	add(&blockers, skewBlocking, "%d nodegroup(s) at the kubelet skew limit")
	if cpFail {
		blockers = append(blockers, "control-plane health check failed")
	}
	add(&warnings, warn, "%d WARNING insight(s)")
	add(&warnings, skewBehind, "%d nodegroup(s) behind the control plane")
	add(&warnings, addonsBehind, "%d addon(s) behind latest")
	if cpWarn {
		warnings = append(warnings, "control-plane health warning")
	}

	var missing []string
	if n := len(r.Failures); n > 0 {
		missing = append(missing, fmt.Sprintf("%d nodegroup(s)/addon(s) could not be read", n))
	}

	switch {
	case len(blockers) > 0:
		return ReadinessBlocked, append(append(blockers, missing...), warnings...)
	case len(missing) > 0:
		return ReadinessIncomplete, append(missing, warnings...)
	case len(warnings) > 0:
		return ReadinessReview, warnings
	default:
		return ReadinessReady, nil
	}
}
