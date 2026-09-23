package nodegroup

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/fatih/color"

	"github.com/dantech2000/refresh/internal/render"
	nodegroupsvc "github.com/dantech2000/refresh/internal/services/nodegroup"
	"github.com/dantech2000/refresh/internal/ui"
)

// outputNodegroupsTable renders the nodegroup list. The human path uses the
// render design system (tokenized STATUS/AMI cells); `-o plain` writes pure
// TSV (header + one row per nodegroup) and sends the empty-list notice to
// stderr.
func outputNodegroupsTable(clusterName string, items []nodegroupsvc.NodegroupSummary) error {
	if ui.PlainOutput() {
		if len(items) == 0 {
			_, _ = fmt.Fprintf(os.Stderr, "No nodegroups found for cluster: %s\n", clusterName)
		}
		nodegroupListPlain(items).Render()
		return nil
	}
	if len(items) == 0 {
		color.Yellow("No nodegroups found for cluster: %s", clusterName)
		return nil
	}
	th := render.Default(os.Stdout)
	for _, line := range nodegroupListLines(th, clusterName, items) {
		fmt.Println(line)
	}
	return nil
}

// nodegroupListPlain builds the `nodegroup list -o plain` table. Headers come
// from nodegroupListColumns, the human table's column set.
func nodegroupListPlain(items []nodegroupsvc.NodegroupSummary) *ui.PlainTable {
	cols := nodegroupListColumns()
	headers := make([]string, len(cols))
	for i, c := range cols {
		headers[i] = c.Title
	}
	t := ui.NewPlainTable(headers...)
	for _, ng := range items {
		t.Row(
			ng.Name,
			ng.Status,
			ng.InstanceType,
			plainVersionCell(ng),
			plainAMICell(ng),
			nodeCountText(ng.ReadyKnown, ng.ReadyNodes, ng.DesiredSize),
		)
	}
	return t
}

// plainVersionCell is the `-o plain` VERSION cell. The human table marks a
// nodegroup behind the control plane with a warning glyph; plain has no
// glyphs, so it gets a "(behind)" marker that grep/awk can find.
func plainVersionCell(ng nodegroupsvc.NodegroupSummary) string {
	if ng.VersionBehind {
		return ng.K8sVersion + " (behind)"
	}
	return orDash(ng.K8sVersion)
}

// plainAMICell is the `-o plain` AMI cell, in the human table's vocabulary.
func plainAMICell(ng nodegroupsvc.NodegroupSummary) string {
	if ng.AMILookupError != "" {
		return amiLookupFailedText
	}
	return ng.AMIStatus.String()
}

// outputNodegroupDetailsTable renders one nodegroup. `-o plain` writes a
// FIELD/VALUE TSV (see nodegroupDetailPlain).
func outputNodegroupDetailsTable(details *nodegroupsvc.NodegroupDetails, elapsed time.Duration) error {
	if ui.PlainOutput() {
		nodegroupDetailPlain(details).Render()
		return nil
	}
	ui.Outf("Nodegroup: %s\n", color.CyanString(details.Name))
	ui.Outf("Retrieved in %s\n\n", ui.ElapsedString(elapsed))

	latestAMI := details.LatestAMI
	amiStatus := details.AMIStatus.PlainString()
	amiStatusColor := func(string) string { return details.AMIStatus.ColorString() }
	if details.AMILookupError != "" {
		latestAMI = amiLookupFailedText
		amiStatus = amiLookupFailedText
		amiStatusColor = func(s string) string { return color.YellowString("%s", s) }
	}

	table := ui.NewDynamicTable()
	table.AddStatus("Status", details.Status).
		Add("Instance", details.InstanceType).
		Add("AMI Type", details.AmiType).
		Add("Capacity", details.CapacityType).
		Add("Current AMI", details.CurrentAMI).
		Add("Latest AMI", latestAMI).
		AddColored("AMI Status", amiStatus, amiStatusColor).
		Add("Scaling", scalingText(details.Scaling))
	table.Render()

	if details.Workloads.TotalPods > 0 || details.Workloads.PodDisruption != "" {
		workloadTable := ui.NewDynamicTable()
		workloadTable.Add("Total Pods", fmt.Sprintf("%d", details.Workloads.TotalPods)).
			Add("Critical Pods", fmt.Sprintf("%d", details.Workloads.CriticalPods)).
			Add("PDBs", details.Workloads.PodDisruption)
		workloadTable.RenderSection("Workloads")
	}

	if len(details.Instances) > 0 {
		ui.Outln()
		ui.Outln("Instances:")
		columns := []ui.Column{
			{Title: "INSTANCE ID", Min: 10, Max: 22, Align: ui.AlignLeft},
			{Title: "TYPE", Min: 10, Max: 0, Align: ui.AlignLeft},
			{Title: "LAUNCH", Min: 10, Max: 0, Align: ui.AlignLeft},
			{Title: "LIFECYCLE", Min: 9, Max: 0, Align: ui.AlignLeft},
			{Title: "STATE", Min: 8, Max: 0, Align: ui.AlignLeft},
		}
		instTable := ui.NewPTable(columns, ui.CyanHeaders())
		for _, inst := range details.Instances {
			instTable.AddRow(
				ui.TruncateANSI(inst.InstanceID, 22),
				inst.InstanceType,
				inst.LaunchTime.Format("2006-01-02"),
				inst.Lifecycle,
				inst.State,
			)
		}
		instTable.Render()
	}
	return nil
}

func scalingText(s nodegroupsvc.ScalingConfig) string {
	return fmt.Sprintf("%d desired (%d-%d)", s.DesiredSize, s.MinSize, s.MaxSize)
}

// nodegroupDetailPlain builds the `nodegroup describe -o plain` FIELD/VALUE
// table: the human view's fields (lowercased), untruncated, then one
// "instance/<id>" row per instance with space-separated key=value pairs.
func nodegroupDetailPlain(d *nodegroupsvc.NodegroupDetails) *ui.PlainTable {
	latestAMI, amiStatus := d.LatestAMI, d.AMIStatus.String()
	if d.AMILookupError != "" {
		latestAMI, amiStatus = amiLookupFailedText, amiLookupFailedText
	}
	t := ui.NewPlainKV()
	t.Add("name", d.Name).
		Add("status", d.Status).
		Add("instance", d.InstanceType).
		Add("ami type", d.AmiType).
		Add("capacity", d.CapacityType).
		Add("current ami", d.CurrentAMI).
		Add("latest ami", latestAMI).
		Add("ami status", amiStatus).
		Add("scaling", scalingText(d.Scaling))
	if d.Workloads.TotalPods > 0 || d.Workloads.PodDisruption != "" {
		t.Add("total pods", fmt.Sprintf("%d", d.Workloads.TotalPods)).
			Add("critical pods", fmt.Sprintf("%d", d.Workloads.CriticalPods)).
			Add("pdbs", d.Workloads.PodDisruption)
	}
	for _, inst := range d.Instances {
		launched := ""
		if !inst.LaunchTime.IsZero() {
			launched = inst.LaunchTime.Format("2006-01-02")
		}
		t.Add("instance/"+inst.InstanceID, ui.PlainPairs(
			"type", inst.InstanceType,
			"launched", launched,
			"lifecycle", inst.Lifecycle,
			"state", inst.State,
			"az", inst.AZ,
		))
	}
	return t
}

func sortNodegroupSummaries(items []nodegroupsvc.NodegroupSummary, key string, desc bool) []nodegroupsvc.NodegroupSummary {
	less := func(i, j int) bool { return false }
	switch strings.ToLower(key) {
	case "status":
		less = func(i, j int) bool { return items[i].Status < items[j].Status }
	case "instance":
		less = func(i, j int) bool { return items[i].InstanceType < items[j].InstanceType }
	case "nodes":
		less = func(i, j int) bool { return items[i].ReadyNodes < items[j].ReadyNodes }
	default:
		less = func(i, j int) bool { return items[i].Name < items[j].Name }
	}
	sort.SliceStable(items, func(i, j int) bool {
		if desc {
			// Swap arguments rather than negating: !less(i,j) returns true
			// for equal elements, which violates the sort contract and
			// destroys SliceStable's stability.
			return less(j, i)
		}
		return less(i, j)
	})
	return items
}
