package statusview

import (
	"fmt"
	"strings"
	"time"

	"github.com/dantech2000/refresh/internal/diag"
	"github.com/dantech2000/refresh/internal/render"
	statussvc "github.com/dantech2000/refresh/internal/services/status"
	"github.com/dantech2000/refresh/internal/ui"
)

// overall collapses a cluster's posture into one status token: unsupported EKS
// is a failure, extended support or stale AMIs/addons is a warning, an
// incomplete row (a data source failed) is unknown, otherwise it's current.
func overall(c statussvc.ClusterStatus) render.Status {
	switch {
	case c.Support.Tier == statussvc.SupportUnsupported:
		return render.Fail
	case c.SupportRisk() || c.NeedsAttention():
		return render.Warn
	case c.Incomplete:
		return render.Unknown
	default:
		return render.Healthy
	}
}

func countIncomplete(statuses []statussvc.ClusterStatus) int {
	n := 0
	for _, c := range statuses {
		if c.Incomplete {
			n++
		}
	}
	return n
}

func distinctRegions(statuses []statussvc.ClusterStatus) int {
	seen := make(map[string]struct{}, len(statuses))
	for _, c := range statuses {
		seen[c.Region] = struct{}{}
	}
	return len(seen)
}

func nameOr(c statussvc.ClusterStatus) string {
	if c.Name == "" {
		return "unknown"
	}
	return c.Name
}

// fleetDataColumns is the fleet table's named columns, shared by the human
// table (which prepends an untitled glyph column) and the `-o plain` header.
func fleetDataColumns() []ui.Column {
	return []ui.Column{
		{Title: "CLUSTER", Min: 8},
		{Title: "REGION", Min: 6},
		{Title: "VERSION", Min: 7},
		{Title: "SUPPORT", Min: 10, Max: 34},
		{Title: "COMPUTE", Min: 8, Max: 26},
		{Title: "STALE AMI", Min: 6},
		{Title: "ADDONS", Min: 6, Max: 26},
		{Title: "HEALTH", Min: 6},
	}
}

// fleetLines builds the human fleet dashboard as a slice of lines (pure, so it
// is golden-testable). th carries the color level / unicode capability.
// failures are the run's failures, listed in the INCOMPLETE DATA section.
func fleetLines(th *render.Theme, statuses []statussvc.ClusterStatus, failures []diag.Failure, elapsed time.Duration) []string {
	pal := th.Pal
	if len(statuses) == 0 {
		// An empty sweep: no table of empty columns, and no "0 region(s)",
		// which reads as if nothing was swept.
		out := []string{th.Bold(pal.Mauve, "FLEET") + "  " + th.Paint(pal.White, "No EKS clusters found")}
		out = append(out, th.FailureSection(failures)...)
		return append(out, "", th.Paint(pal.Dim, fmt.Sprintf("0 clusters  (%s)", elapsed.Round(time.Millisecond))))
	}
	out := []string{
		th.Bold(pal.Mauve, "FLEET") + "  " +
			th.Paint(pal.White, render.Plural(len(statuses), "cluster")) +
			th.Paint(pal.Dim, " · "+render.Plural(distinctRegions(statuses), "region")),
		"",
		chipsLine(th, statuses),
		"",
	}

	tbl := th.NewTable(append([]ui.Column{{Title: "", Min: 1}}, fleetDataColumns()...)...)
	for _, c := range statuses {
		version := c.Version
		if version == "" {
			version = "unknown"
		}
		tbl.Row(
			th.Glyph(overall(c)),
			th.Paint(pal.White, nameOr(c)),
			th.Paint(pal.Dim, c.Region),
			th.Paint(pal.White, version),
			supportPretty(th, c.Support),
			computePretty(th, c),
			stalePretty(th, c),
			addonsPretty(th, c.AddonsBehind),
			healthPretty(th, c.HealthIssues),
		)
	}
	out = append(out, tbl.Render()...)
	out = append(out, th.FailureSection(failures)...)
	out = append(out, "", footerPretty(th, statuses, elapsed))
	if h := hintLine(th, statuses); h != "" {
		out = append(out, "", h)
	}
	return out
}

func chipsLine(th *render.Theme, statuses []statussvc.ClusterStatus) string {
	var healthy, warn, fail, unknown int
	for _, c := range statuses {
		switch overall(c) {
		case render.Healthy:
			healthy++
		case render.Warn:
			warn++
		case render.Fail:
			fail++
		case render.Unknown:
			unknown++
		}
	}
	parts := []string{th.Token(render.Healthy, fmt.Sprintf("%d current", healthy))}
	if warn > 0 {
		parts = append(parts, th.Token(render.Warn, fmt.Sprintf("%d need attention", warn)))
	}
	if fail > 0 {
		parts = append(parts, th.Token(render.Fail, fmt.Sprintf("%d unsupported", fail)))
	}
	if unknown > 0 {
		parts = append(parts, th.Token(render.Unknown, fmt.Sprintf("%d incomplete", unknown)))
	}
	return strings.Join(parts, "   ")
}

func supportPretty(th *render.Theme, s statussvc.SupportPosture) string {
	switch s.Tier {
	case statussvc.SupportStandard:
		txt := "standard"
		if s.DaysRemaining != nil {
			txt += fmt.Sprintf(" (%dd)", *s.DaysRemaining)
		}
		return th.Paint(th.Pal.Green, txt)
	case statussvc.SupportExtended:
		txt := "extended"
		if s.DaysRemaining != nil {
			txt += fmt.Sprintf(" (%dd)", *s.DaysRemaining)
		}
		return th.Token(render.Warn, txt)
	case statussvc.SupportUnsupported:
		return th.Token(render.Fail, "unsupported")
	default:
		return th.Paint(th.Pal.Dim, "unknown")
	}
}

func computePretty(th *render.Theme, c statussvc.ClusterStatus) string {
	switch c.Compute {
	case statussvc.ComputeManaged:
		return th.Paint(th.Pal.Dim, render.Plural(c.NodegroupCount, "nodegroup"))
	case statussvc.ComputeAutoMode:
		return th.Paint(th.Pal.Teal, autoModeText(c))
	case statussvc.ComputeKarpenter:
		return th.Paint(th.Pal.Teal, "Karpenter")
	default:
		return th.Paint(th.Pal.Dim, "none")
	}
}

func stalePretty(th *render.Theme, c statussvc.ClusterStatus) string {
	if !hasNodegroupAMIs(c) {
		return th.Paint(th.Pal.Dim, "n/a")
	}
	if c.StaleAMI.Behind == 0 && c.NodegroupsBehindControlPlane == 0 {
		return th.Paint(th.Pal.Green, "0")
	}
	return th.Token(render.Warn, staleAMIText(c))
}

// staleAMIText is the uncolored STALE AMI text for a managed-nodegroup
// cluster: "0", or "behind/total (oldest Nd)" plus any nodegroups behind the
// control plane.
func staleAMIText(c statussvc.ClusterStatus) string {
	txt := "0"
	if c.StaleAMI.Behind > 0 {
		txt = fmt.Sprintf("%d/%d", c.StaleAMI.Behind, c.StaleAMI.Total)
		if c.StaleAMI.OldestDays != nil {
			txt += fmt.Sprintf(" (%dd)", *c.StaleAMI.OldestDays)
		}
	}
	return txt + behindCPSuffix(c)
}

// behindCPSuffix names nodegroups on an older Kubernetes minor than the
// control plane. AMI freshness is judged per nodegroup minor, so without it a
// half-finished upgrade shows "0" stale and no reason for the warning.
func behindCPSuffix(c statussvc.ClusterStatus) string {
	if c.NodegroupsBehindControlPlane == 0 {
		return ""
	}
	return fmt.Sprintf(" · %d behind CP", c.NodegroupsBehindControlPlane)
}

func addonsPretty(th *render.Theme, a statussvc.AddonsBehindSummary) string {
	if a.Behind == 0 {
		return th.Paint(th.Pal.Green, "0")
	}
	names := a.Names
	suffix := ""
	const maxNames = 2
	if len(names) > maxNames {
		suffix = fmt.Sprintf(" +%d", len(names)-maxNames)
		names = names[:maxNames]
	}
	return th.Token(render.Warn, fmt.Sprintf("%d (%s%s)", a.Behind, strings.Join(names, ","), suffix))
}

// healthPretty is the HEALTH cell: the count of AWS-reported control-plane
// health issues. Such issues alone make status exit 2, so the row must show
// why it needs attention.
func healthPretty(th *render.Theme, issues int) string {
	if issues == 0 {
		return th.Paint(th.Pal.Green, "0")
	}
	return th.Token(render.Warn, healthCell(issues))
}

func footerPretty(th *render.Theme, statuses []statussvc.ClusterStatus, elapsed time.Duration) string {
	staleNG, addonsBehind, supportRisk, ngBehindCP := 0, 0, 0, 0
	for _, c := range statuses {
		staleNG += c.StaleAMI.Behind
		addonsBehind += c.AddonsBehind.Behind
		ngBehindCP += c.NodegroupsBehindControlPlane
		if c.SupportRisk() {
			supportRisk++
		}
	}
	txt := fmt.Sprintf("%s · %s · %s behind · %d extended/unsupported",
		render.Plural(len(statuses), "cluster"), render.Plural(staleNG, "stale nodegroup"), render.Plural(addonsBehind, "addon"), supportRisk)
	if ngBehindCP > 0 {
		txt += " · " + render.Plural(ngBehindCP, "nodegroup") + " behind control plane"
	}
	incomplete := countIncomplete(statuses)
	if incomplete > 0 {
		txt += fmt.Sprintf(" · %d incomplete", incomplete)
	}
	clean := staleNG == 0 && addonsBehind == 0 && supportRisk == 0 && ngBehindCP == 0 && incomplete == 0
	line := th.Paint(th.Pal.Dim, txt)
	if clean {
		line = th.Paint(th.Pal.Green, txt)
	}
	if elapsed > 0 {
		line += th.Paint(th.Pal.Dim, fmt.Sprintf("  (%s)", elapsed.Round(time.Millisecond)))
	}
	return line
}

// hintLine points at the most urgent cluster (a failure first, else a warning)
// with the command to dig in, in that cluster's region.
func hintLine(th *render.Theme, statuses []statussvc.ClusterStatus) string {
	var worst *statussvc.ClusterStatus
	for i := range statuses {
		if overall(statuses[i]) == render.Fail {
			worst = &statuses[i]
			break
		}
	}
	if worst == nil {
		for i := range statuses {
			if overall(statuses[i]) == render.Warn {
				worst = &statuses[i]
				break
			}
		}
	}
	if worst == nil {
		return ""
	}
	st := overall(*worst)
	reason := "needs attention"
	switch {
	case st == render.Fail:
		reason = "is on unsupported EKS"
	case worst.HealthIssues > 0:
		reason = fmt.Sprintf("has %d control-plane health issue(s)", worst.HealthIssues)
	case worst.NodegroupsBehindControlPlane > 0:
		reason = fmt.Sprintf("has %d nodegroup(s) behind the control plane", worst.NodegroupsBehindControlPlane)
	case worst.NeedsAttention():
		reason = "has stale AMIs/addons"
	}
	name := nameOr(*worst)
	return th.Glyph(st) + " " + th.Paint(th.Pal.White, name) +
		th.Paint(th.Pal.Dim, " "+reason+" → ") +
		th.Paint(th.Pal.Blue, upgradeCheckCommand(*worst))
}

// upgradeCheckCommand is the hint's command for one cluster. It names the
// cluster's region: after a multi-region sweep the cluster may not be in the
// default region, and upgrade-check would not find it there.
func upgradeCheckCommand(c statussvc.ClusterStatus) string {
	cmd := "refresh cluster upgrade-check -c " + nameOr(c)
	if c.Region != "" {
		cmd += " -r " + c.Region
	}
	return cmd
}
