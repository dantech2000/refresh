package nodegroup

import (
	"fmt"
	"time"

	"github.com/dantech2000/refresh/internal/render"
	nodegroupsvc "github.com/dantech2000/refresh/internal/services/nodegroup"
	"github.com/dantech2000/refresh/internal/ui"
)

// nodegroupDetailLines builds the human `nodegroup describe` view (pure, so
// tests can check it): a name/status header, OVERVIEW key/values with the AMI
// status as a token, and the WORKLOADS and INSTANCES sections when they were
// read.
func nodegroupDetailLines(th *render.Theme, d *nodegroupsvc.NodegroupDetails, elapsed time.Duration) []string {
	pal := th.Pal
	out := []string{
		th.Bold(pal.White, d.Name) + "   " + th.Token(render.StatusFromString(d.Status), orDash(d.Status)) +
			th.Paint(pal.Dim, fmt.Sprintf("   retrieved in %.1fs", elapsed.Seconds())),
		"",
		th.Section("OVERVIEW"),
	}

	latestAMI, amiStatus := d.LatestAMI, amiToken(th, d.AMIStatus)
	if d.AMILookupFailure != nil {
		latestAMI = amiLookupFailedText
		amiStatus = th.Token(render.Warn, amiLookupFailedText)
	}
	kv := [][2]string{
		{"instance", th.Paint(pal.Text, orDash(d.InstanceType))},
		{"ami type", th.Paint(pal.Text, orDash(d.AmiType))},
		{"capacity", th.Paint(pal.Text, orDash(d.CapacityType))},
		{"current ami", th.Paint(pal.Text, orDash(d.CurrentAMI))},
		{"latest ami", th.Paint(pal.Text, orDash(latestAMI))},
		{"ami status", amiStatus},
		{"scaling", th.Paint(pal.Text, scalingText(d.Scaling))},
	}
	for _, l := range th.KV(kv) {
		out = append(out, "  "+l)
	}

	if d.Workloads.TotalPods > 0 || d.Workloads.PodDisruption != "" {
		out = append(out, "", th.Section("WORKLOADS"))
		for _, l := range th.KV([][2]string{
			{"total pods", th.Paint(pal.Text, fmt.Sprintf("%d", d.Workloads.TotalPods))},
			{"critical pods", th.Paint(pal.Text, fmt.Sprintf("%d", d.Workloads.CriticalPods))},
			{"pdbs", th.Paint(pal.Text, orDash(d.Workloads.PodDisruption))},
		}) {
			out = append(out, "  "+l)
		}
	}

	if insts := instanceList(d); len(insts) > 0 {
		out = append(out, "", th.Section("INSTANCES")+th.Paint(pal.Dim, fmt.Sprintf("  %d", len(insts))))
		tbl := th.NewTable(
			ui.Column{Title: "INSTANCE ID", Min: 10, Max: 22},
			ui.Column{Title: "TYPE", Min: 10},
			ui.Column{Title: "LAUNCH", Min: 10},
			ui.Column{Title: "LIFECYCLE", Min: 9},
			ui.Column{Title: "STATE", Min: 8},
		)
		for _, inst := range insts {
			launched := "-"
			if !inst.LaunchTime.IsZero() {
				launched = inst.LaunchTime.Format("2006-01-02")
			}
			tbl.Row(
				th.Paint(pal.White, inst.InstanceID),
				th.Paint(pal.Text, inst.InstanceType),
				th.Paint(pal.Text, launched),
				th.Paint(pal.Text, orDash(inst.Lifecycle)),
				th.Token(render.StatusFromString(inst.State), orDash(inst.State)),
			)
		}
		for _, l := range tbl.Render() {
			out = append(out, "  "+l)
		}
	}
	return out
}
