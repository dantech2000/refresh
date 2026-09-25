package nodegroup

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/dantech2000/refresh/internal/diag"
	"github.com/dantech2000/refresh/internal/render"
	nodegroupsvc "github.com/dantech2000/refresh/internal/services/nodegroup"
	"github.com/dantech2000/refresh/internal/ui"
)

// outputNodegroupsTable renders the nodegroup list. The human path uses the
// render design system (tokenized STATUS/AMI cells, and the INCOMPLETE DATA
// section for failures); `-o plain` writes pure TSV (header + one row per
// nodegroup) and sends the empty-list notice to stderr. The caller reports
// failures on stderr for -o plain.
func outputNodegroupsTable(clusterName string, items []nodegroupsvc.NodegroupSummary, failures []diag.Failure) error {
	if ui.PlainOutput() {
		if len(items) == 0 {
			_, _ = fmt.Fprintf(ui.Stderr, "No nodegroups found for cluster: %s\n", clusterName)
		}
		nodegroupListPlain(items).Render()
		return nil
	}
	th := render.Default(os.Stdout)
	lines := th.FailureSection(failures)
	if len(items) == 0 {
		lines = append([]string{th.Line(render.Neutral, "No nodegroups found for cluster: %s", clusterName)}, lines...)
	} else {
		lines = append(nodegroupListLines(th, clusterName, items), lines...)
	}
	for _, line := range lines {
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
	if ng.AMILookupFailure != nil {
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
	for _, l := range nodegroupDetailLines(render.Default(os.Stdout), details, elapsed) {
		fmt.Println(l)
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
	if d.AMILookupFailure != nil {
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
	for _, inst := range instanceList(d) {
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
		less = func(i, j int) bool { return nodeSortKey(items[i]) < nodeSortKey(items[j]) }
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

// nodeSortKey is the count the NODES column shows: the measured Ready nodes
// with -R (--check-readiness), else the desired size. ReadyNodes is 0 when
// readiness was not measured, so sorting on it alone would leave the list
// unsorted.
func nodeSortKey(ng nodegroupsvc.NodegroupSummary) int32 {
	if ng.ReadyKnown {
		return ng.ReadyNodes
	}
	return ng.DesiredSize
}

// instanceList returns the instances that were read (nil when they were
// not).
func instanceList(d *nodegroupsvc.NodegroupDetails) []nodegroupsvc.InstanceDetails {
	if d.Instances == nil {
		return nil
	}
	return *d.Instances
}
