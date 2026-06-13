package statusview

import (
	"fmt"
	"strings"
	"time"

	"github.com/dantech2000/refresh/internal/render"
	statussvc "github.com/dantech2000/refresh/internal/services/status"
	"github.com/dantech2000/refresh/internal/ui"
)

// overall collapses a cluster's posture into one status token: unsupported EKS
// is a failure, extended support or stale AMIs/addons is a warning, otherwise
// it's current.
func overall(c statussvc.ClusterStatus) render.Status {
	switch {
	case c.Support.Tier == statussvc.SupportUnsupported:
		return render.Fail
	case c.SupportRisk() || c.NeedsAttention():
		return render.Warn
	default:
		return render.Healthy
	}
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

// fleetLines builds the human fleet dashboard as a slice of lines (pure, so it
// is golden-testable). th carries the color level / unicode capability.
func fleetLines(th *render.Theme, statuses []statussvc.ClusterStatus, elapsed time.Duration) []string {
	pal := th.Pal
	out := []string{
		th.Bold(pal.Mauve, "FLEET") + "  " +
			th.Paint(pal.White, fmt.Sprintf("%d clusters", len(statuses))) +
			th.Paint(pal.Dim, fmt.Sprintf(" · %d region(s)", distinctRegions(statuses))),
		"",
		chipsLine(th, statuses),
		"",
	}

	tbl := th.NewTable(
		ui.Column{Title: "", Min: 1},
		ui.Column{Title: "CLUSTER", Min: 8},
		ui.Column{Title: "REGION", Min: 6},
		ui.Column{Title: "VERSION", Min: 7},
		ui.Column{Title: "SUPPORT", Min: 10, Max: 34},
		ui.Column{Title: "COMPUTE", Min: 8, Max: 22},
		ui.Column{Title: "STALE AMI", Min: 6},
		ui.Column{Title: "ADDONS", Min: 6, Max: 26},
	)
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
		)
	}
	out = append(out, tbl.Render()...)
	out = append(out, "", footerPretty(th, statuses, elapsed))
	if h := hintLine(th, statuses); h != "" {
		out = append(out, "", h)
	}
	return out
}

func chipsLine(th *render.Theme, statuses []statussvc.ClusterStatus) string {
	var healthy, warn, fail int
	for _, c := range statuses {
		switch overall(c) {
		case render.Healthy:
			healthy++
		case render.Warn:
			warn++
		case render.Fail:
			fail++
		}
	}
	parts := []string{th.Token(render.Healthy, fmt.Sprintf("%d current", healthy))}
	if warn > 0 {
		parts = append(parts, th.Token(render.Warn, fmt.Sprintf("%d need attention", warn)))
	}
	if fail > 0 {
		parts = append(parts, th.Token(render.Fail, fmt.Sprintf("%d unsupported", fail)))
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
		return th.Paint(th.Pal.Dim, fmt.Sprintf("%d nodegroups", c.NodegroupCount))
	case statussvc.ComputeAutoMode:
		return th.Paint(th.Pal.Teal, "Auto Mode")
	case statussvc.ComputeKarpenter:
		return th.Paint(th.Pal.Teal, "Karpenter")
	default:
		return th.Paint(th.Pal.Dim, "none")
	}
}

func stalePretty(th *render.Theme, c statussvc.ClusterStatus) string {
	if c.Compute != statussvc.ComputeManaged {
		return th.Paint(th.Pal.Dim, "n/a")
	}
	if c.StaleAMI.Behind == 0 {
		return th.Paint(th.Pal.Green, "0")
	}
	txt := fmt.Sprintf("%d/%d", c.StaleAMI.Behind, c.StaleAMI.Total)
	if c.StaleAMI.OldestDays != nil {
		txt += fmt.Sprintf(" (%dd)", *c.StaleAMI.OldestDays)
	}
	return th.Token(render.Warn, txt)
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

func footerPretty(th *render.Theme, statuses []statussvc.ClusterStatus, elapsed time.Duration) string {
	staleNG, addonsBehind, supportRisk := 0, 0, 0
	for _, c := range statuses {
		staleNG += c.StaleAMI.Behind
		addonsBehind += c.AddonsBehind.Behind
		if c.SupportRisk() {
			supportRisk++
		}
	}
	txt := fmt.Sprintf("%d clusters · %d stale nodegroups · %d addons behind · %d extended/unsupported",
		len(statuses), staleNG, addonsBehind, supportRisk)
	clean := staleNG == 0 && addonsBehind == 0 && supportRisk == 0
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
// with the command to dig in.
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
	case worst.NeedsAttention():
		reason = "has stale AMIs/addons"
	}
	name := nameOr(*worst)
	return th.Glyph(st) + " " + th.Paint(th.Pal.White, name) +
		th.Paint(th.Pal.Dim, " "+reason+" → ") +
		th.Paint(th.Pal.Blue, "refresh cluster upgrade-check -c "+name)
}
