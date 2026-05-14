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

func outputNodegroupsTable(clusterName, timeframe string, items []nodegroupsvc.NodegroupSummary, elapsed time.Duration, opts nodegroupsvc.ListOptions) error {
	if len(items) == 0 {
		color.Yellow("No nodegroups found for cluster: %s", clusterName)
		return nil
	}
	ui.Outf("Nodegroups for cluster: %s\n", clusterName)
	if opts.ShowUtilization {
		ui.Outf("Retrieved in %s (utilization window %s)\n", color.GreenString("%.1fs", elapsed.Seconds()), timeframe)
	} else {
		ui.Outf("Retrieved in %s\n", color.GreenString("%.1fs", elapsed.Seconds()))
	}

	if opts.ShowUtilization || opts.ShowCosts {
		var extras []string
		if opts.ShowUtilization {
			extras = append(extras, "CPU metrics")
		}
		if opts.ShowCosts {
			extras = append(extras, "cost estimates")
		}
		ui.Outf("Including: %s\n", strings.Join(extras, ", "))
	}
	ui.Outln()

	columns := []ui.Column{
		{Title: "NAME", Min: 4, Max: 60, Align: ui.AlignLeft},
		{Title: "STATUS", Min: 10, Max: 0, Align: ui.AlignLeft},
		{Title: "INSTANCE", Min: 10, Max: 0, Align: ui.AlignLeft},
		{Title: "AMI STATUS", Min: 9, Max: 0, Align: ui.AlignLeft},
		{Title: "NODES", Min: 7, Max: 0, Align: ui.AlignRight},
	}
	if opts.ShowUtilization {
		columns = append(columns, ui.Column{Title: "CPU%", Min: 5, Max: 0, Align: ui.AlignRight})
	}
	if opts.ShowCosts {
		columns = append(columns, ui.Column{Title: "COST/MO", Min: 8, Max: 0, Align: ui.AlignRight})
	}

	table := ui.NewPTable(columns, ui.WithPTableHeaderColor(func(s string) string { return color.CyanString(s) }))
	for _, ng := range items {
		row := []string{
			ng.Name,
			ng.Status,
			ng.InstanceType,
			ng.AMIStatus.String(),
			fmt.Sprintf("%d/%d", ng.ReadyNodes, ng.DesiredSize),
		}
		if opts.ShowUtilization {
			cpu := "-"
			if ng.Metrics.CPU > 0 {
				cpu = fmt.Sprintf("%.0f%%", ng.Metrics.CPU)
			}
			row = append(row, cpu)
		}
		if opts.ShowCosts {
			cost := "-"
			if ng.Cost.Monthly > 0 {
				cost = fmt.Sprintf("$%.0f", ng.Cost.Monthly)
			}
			row = append(row, cost)
		}
		table.AddRow(row...)
	}
	table.Render()
	return nil
}

func outputNodegroupDetailsTable(details *nodegroupsvc.NodegroupDetails, elapsed time.Duration) error {
	ui.Outf("Nodegroup: %s\n", color.CyanString(details.Name))
	if details.Utilization.TimeRange != "" {
		ui.Outf("Retrieved in %s (utilization window %s)\n\n", color.GreenString("%.1fs", elapsed.Seconds()), details.Utilization.TimeRange)
	} else {
		ui.Outf("Retrieved in %s\n\n", color.GreenString("%.1fs", elapsed.Seconds()))
	}

	table := ui.NewDynamicTable()
	table.AddStatus("Status", details.Status).
		Add("Instance", details.InstanceType).
		Add("AMI Type", details.AmiType).
		Add("Capacity", details.CapacityType).
		Add("Current AMI", details.CurrentAMI).
		Add("Latest AMI", details.LatestAMI).
		AddColored("AMI Status", details.AMIStatus.String(), func(s string) string { return details.AMIStatus.String() }).
		Add("Scaling", fmt.Sprintf("%d desired (%d-%d)", details.Scaling.DesiredSize, details.Scaling.MinSize, details.Scaling.MaxSize))

	if details.Utilization.TimeRange != "" || (details.Utilization.CPU.Average > 0 || details.Utilization.CPU.Current > 0) {
		avg := details.Utilization.CPU.Average
		cur := details.Utilization.CPU.Current
		peak := details.Utilization.CPU.Peak
		table.Add("CPU (avg)", fmt.Sprintf("%.1f%%", avg))
		if cur > 0 {
			table.Add("CPU (current)", fmt.Sprintf("%.1f%%", cur))
		}
		if peak > 0 {
			table.Add("CPU (peak)", fmt.Sprintf("%.1f%%", peak))
		}
	}
	if details.Cost.CostPerNode > 0 {
		table.Add("Cost per node", fmt.Sprintf("$%.0f/mo", details.Cost.CostPerNode))
	}
	if details.Cost.CurrentMonthlyCost > 0 {
		table.Add("Cost/month", fmt.Sprintf("$%.0f", details.Cost.CurrentMonthlyCost))
	}
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
		instTable := ui.NewPTable(columns, ui.WithPTableHeaderColor(func(s string) string { return color.CyanString(s) }))
		for _, inst := range details.Instances {
			instTable.AddRow(
				truncate(inst.InstanceID, 22),
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
	case "cpu":
		less = func(i, j int) bool { return items[i].Metrics.CPU < items[j].Metrics.CPU }
	case "cost":
		less = func(i, j int) bool { return items[i].Cost.Monthly < items[j].Cost.Monthly }
	default:
		less = func(i, j int) bool { return items[i].Name < items[j].Name }
	}
	sort.SliceStable(items, func(i, j int) bool {
		if desc {
			return !less(i, j)
		}
		return less(i, j)
	})
	return items
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}
