package clusterview

import (
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/dantech2000/refresh/internal/health"
	clustersvc "github.com/dantech2000/refresh/internal/services/cluster"
	"github.com/dantech2000/refresh/internal/ui"
)

// `-o plain` renderers for the cluster views. Each one builds a ui.PlainTable
// (header + one TSV row per item, see internal/ui/plain.go). List headers come
// from the same column definitions as the human table, so the two never drift.

const plainTimeLayout = "2006-01-02 15:04:05 UTC"

// columnTitles returns the header titles of cols.
func columnTitles(cols []ui.Column) []string {
	out := make([]string, len(cols))
	for i, c := range cols {
		out[i] = c.Title
	}
	return out
}

// clusterListPlain builds the `cluster list -o plain` table. Cells use the
// human table's vocabulary: the raw EKS status, the health decision, and the
// NODES count ("unknown" for an incomplete row with no count).
func clusterListPlain(summaries []clustersvc.ClusterSummary, multiRegion, showHealth bool) *ui.PlainTable {
	t := ui.NewPlainTable(columnTitles(clusterListColumns(multiRegion, showHealth))...)
	for _, s := range summaries {
		row := []string{s.Name}
		if multiRegion {
			row = append(row, s.Region)
		}
		row = append(row, s.Status, s.Version)
		if showHealth {
			h := ""
			if s.Health != nil {
				h = decisionLabel(s.Health.Decision)
			}
			row = append(row, h)
		}
		row = append(row, nodesText(s))
		t.Row(row...)
	}
	return t
}

// clusterDetailPlain builds the `cluster describe -o plain` FIELD/VALUE
// table. It carries every value the human view shows, untruncated, plus the
// data the human view only summarizes (subnet and security group IDs, deletion
// protection). Repeated items use "<kind>/<name>" fields, and their values are
// space-separated key=value pairs.
func clusterDetailPlain(d *clustersvc.ClusterDetails) *ui.PlainTable {
	t := ui.NewPlainKV()
	t.Add("name", d.Name)
	t.Add("region", d.Region)
	t.Add("status", d.Status)
	t.Add("version", d.Version)
	t.Add("platform version", d.PlatformVersion)
	if d.Support != nil {
		t.Add("support", supportPlain(d.Support))
	}
	t.Add("endpoint", d.Endpoint)
	if d.Networking.VpcID != "" {
		vpc := d.Networking.VpcID
		if d.Networking.VpcCidr != "" {
			vpc += " (" + d.Networking.VpcCidr + ")"
		}
		t.Add("vpc", vpc)
		t.Add("subnets", strings.Join(d.Networking.SubnetIDs, ","))
		t.Add("security groups", strings.Join(d.Networking.SecurityGroupIDs, ","))
	}
	logging := "disabled"
	if len(d.Security.LoggingEnabled) > 0 {
		logging = strings.Join(d.Security.LoggingEnabled, ",") + " enabled"
	}
	t.Add("logging", logging)
	if d.Security.EncryptionEnabled {
		t.Add("encryption", "enabled (KMS)")
	} else {
		t.Add("encryption", "disabled")
	}
	if d.Security.DeletionProtection {
		t.Add("deletion protection", "enabled")
	} else {
		t.Add("deletion protection", "disabled")
	}
	if d.CreatedAt.IsZero() {
		t.Add("created", "unknown")
		t.Add("age", "unknown")
	} else {
		t.Add("created", d.CreatedAt.UTC().Format(plainTimeLayout))
		t.Add("age", formatAge(time.Since(d.CreatedAt)))
	}

	if len(d.Nodegroups) > 0 {
		active, nodes := 0, int32(0)
		for _, ng := range d.Nodegroups {
			nodes += ng.DesiredSize
			if ng.Status == "ACTIVE" {
				active++
			}
		}
		t.Add("nodegroups", fmt.Sprintf("%d active, %d nodes", active, nodes))
		for _, ng := range d.Nodegroups {
			t.Add("nodegroup/"+ng.Name, ui.PlainPairs(
				"instance", ng.InstanceType,
				"nodes", nodeCountText(ng.ReadyKnown, ng.ReadyNodes, ng.DesiredSize),
				"status", ng.Status,
			))
		}
	}

	if len(d.Addons) > 0 {
		t.Add("addons", fmt.Sprintf("%d installed", len(d.Addons)))
		for _, a := range d.Addons {
			h := string(a.Health)
			if h == "" {
				h = "Unknown"
			}
			t.Add("addon/"+a.Name, ui.PlainPairs("version", a.Version, "status", a.Status, "health", h))
		}
	}

	for _, iss := range d.HealthIssues {
		v := iss.Message
		if len(iss.ResourceIDs) > 0 {
			v += " [" + strings.Join(iss.ResourceIDs, ", ") + "]"
		}
		t.Add("health issue/"+iss.Code, v)
	}

	if d.Health != nil {
		t.Add("health", healthPlain(d.Health))
		addHealthCheckRows(t, d.Health.Results)
	}
	return t
}

// healthPlain is the uncolored health card headline: decision, score, and
// the first error or warning.
func healthPlain(h *health.HealthSummary) string {
	return fmt.Sprintf("%s (%d/100): %s", decisionLabel(h.Decision), h.OverallScore, healthSummaryMsg(h))
}

// addHealthCheckRows adds one "health check/<name>" row per check.
func addHealthCheckRows(t *ui.PlainTable, results []health.HealthResult) {
	for _, r := range results {
		st := healthStatusLabel(r.Status)
		if r.Skipped {
			st = "SKIPPED"
		}
		t.Add("health check/"+r.Name, st+": "+r.Message)
	}
}

// upgradeCheckPlain builds the `cluster upgrade-check -o plain` table: one row
// per insight, with the full insight ID (the human table shortens it).
func upgradeCheckPlain(report *clustersvc.UpgradeReport) *ui.PlainTable {
	t := ui.NewPlainTable(columnTitles(insightColumns())...)
	for _, in := range report.Insights {
		refresh := ""
		if in.LastRefreshTime != nil {
			refresh = in.LastRefreshTime.Format(insightTimeLayout)
		}
		t.Row(in.ID, in.Name, in.Category, in.Status, in.KubernetesVersion, refresh)
	}
	return t
}

// writeUpgradeCheckInfo writes the parts of the upgrade-check report that are
// not insight rows (verdict, support, control plane, version skew) to w —
// stderr under `-o plain`, so stdout stays pure TSV.
func writeUpgradeCheckInfo(w io.Writer, report *clustersvc.UpgradeReport) {
	_, verdict := upgradeVerdict(report)
	_, _ = fmt.Fprintf(w, "upgrade readiness for %s: %s\n", report.Cluster, verdict)
	if report.Support != nil {
		_, _ = fmt.Fprintf(w, "support: %s\n", supportPlain(report.Support))
	}
	if cp := report.ControlPlane; cp != nil {
		if cp.Skipped {
			_, _ = fmt.Fprintf(w, "control plane: %s\n", cp.Message)
		} else {
			_, _ = fmt.Fprintf(w, "control plane (%s): %s\n", healthStatusLabel(cp.Status), cp.Message)
			for _, d := range cp.Details {
				_, _ = fmt.Fprintf(w, "  %s\n", d)
			}
		}
	}
	_, _ = fmt.Fprintf(w, "version skew (control plane %s): ", valueOrDash(report.Skew.ControlPlaneVersion))
	if len(report.Skew.Findings) == 0 {
		_, _ = fmt.Fprintln(w, "nodegroups and addons are current")
	} else {
		_, _ = fmt.Fprintf(w, "%d finding(s)\n", len(report.Skew.Findings))
		for _, f := range report.Skew.Findings {
			_, _ = fmt.Fprintf(w, "  %s\n", f)
		}
	}
}

// insightDetailPlain builds the `cluster upgrade-check --id -o plain`
// FIELD/VALUE table.
func insightDetailPlain(d *clustersvc.InsightDetail) *ui.PlainTable {
	t := ui.NewPlainKV()
	t.Add("id", d.ID)
	t.Add("name", d.Name)
	t.Add("status", d.Status)
	if d.StatusReason != "" {
		t.Add("reason", d.StatusReason)
	}
	t.Add("category", d.Category)
	if d.KubernetesVersion != "" {
		t.Add("k8s", d.KubernetesVersion)
	}
	if d.LastRefreshTime != nil {
		t.Add("last refresh", d.LastRefreshTime.Format(insightTimeLayout))
	}
	if d.Description != "" {
		t.Add("description", oneLine(d.Description))
	}
	if d.Recommendation != "" {
		t.Add("recommendation", oneLine(d.Recommendation))
	}
	for _, r := range d.Resources {
		t.Add("resource", r)
	}
	keys := make([]string, 0, len(d.AdditionalInfo))
	for k := range d.AdditionalInfo {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		t.Add("info/"+k, d.AdditionalInfo[k])
	}
	for _, dep := range d.Deprecations {
		t.Add("deprecated api/"+valueOrDash(dep.Usage), ui.PlainPairs(
			"replacement", dep.ReplacedWith,
			"removed-in", dep.StopServingVersion,
		))
		for _, c := range dep.ClientStats {
			last := "-"
			if c.LastRequestTime != nil {
				last = c.LastRequestTime.Format(insightTimeLayout)
			}
			t.Add("deprecated api client/"+valueOrDash(dep.Usage), fmt.Sprintf("%s: %d req/30d, last seen %s",
				valueOrDash(c.UserAgent), c.NumberOfRequestsLast30Days, last))
		}
	}
	return t
}
