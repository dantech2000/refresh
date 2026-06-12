package nodegroup

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/fatih/color"

	nodegroupsvc "github.com/dantech2000/refresh/internal/services/nodegroup"
	"github.com/dantech2000/refresh/internal/ui"
)

func outputNodegroupsTable(clusterName string, items []nodegroupsvc.NodegroupSummary, elapsed time.Duration) error {
	if len(items) == 0 {
		color.Yellow("No nodegroups found for cluster: %s", clusterName)
		return nil
	}
	ui.Outf("Nodegroups for cluster: %s\n", clusterName)
	ui.Outf("Retrieved in %s\n", ui.ElapsedString(elapsed))
	ui.Outln()

	columns := []ui.Column{
		{Title: "NAME", Min: 4, Max: 60, Align: ui.AlignLeft},
		{Title: "STATUS", Min: 10, Max: 0, Align: ui.AlignLeft},
		{Title: "INSTANCE", Min: 10, Max: 0, Align: ui.AlignLeft},
		{Title: "AMI STATUS", Min: 9, Max: 0, Align: ui.AlignLeft},
		{Title: "NODES", Min: 7, Max: 0, Align: ui.AlignRight},
	}

	table := ui.NewPTable(columns, ui.CyanHeaders())
	for _, ng := range items {
		table.AddRow(
			ng.Name,
			ng.Status,
			ng.InstanceType,
			ng.AMIStatus.ColorString(),
			fmt.Sprintf("%d/%d", ng.ReadyNodes, ng.DesiredSize),
		)
	}
	table.Render()
	return nil
}

func outputNodegroupDetailsTable(details *nodegroupsvc.NodegroupDetails, elapsed time.Duration) error {
	ui.Outf("Nodegroup: %s\n", color.CyanString(details.Name))
	ui.Outf("Retrieved in %s\n\n", ui.ElapsedString(elapsed))

	table := ui.NewDynamicTable()
	table.AddStatus("Status", details.Status).
		Add("Instance", details.InstanceType).
		Add("AMI Type", details.AmiType).
		Add("Capacity", details.CapacityType).
		Add("Current AMI", details.CurrentAMI).
		Add("Latest AMI", details.LatestAMI).
		AddColored("AMI Status", details.AMIStatus.PlainString(), func(string) string { return details.AMIStatus.ColorString() }).
		Add("Scaling", fmt.Sprintf("%d desired (%d-%d)", details.Scaling.DesiredSize, details.Scaling.MinSize, details.Scaling.MaxSize))
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
