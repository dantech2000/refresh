package nodegroup

import (
	"fmt"

	"github.com/dantech2000/refresh/internal/render"
	nodegroupsvc "github.com/dantech2000/refresh/internal/services/nodegroup"
	"github.com/dantech2000/refresh/internal/types"
	"github.com/dantech2000/refresh/internal/ui"
)

// nodegroupListLines builds the human `nodegroup list` table (pure,
// golden-testable) with tokenized STATUS and AMI cells.
func nodegroupListLines(th *render.Theme, cluster string, items []nodegroupsvc.NodegroupSummary) []string {
	pal := th.Pal
	out := []string{
		th.Bold(pal.Mauve, "NODEGROUPS") + "  " + th.Paint(pal.White, cluster) +
			th.Paint(pal.Dim, fmt.Sprintf(" · %d", len(items))),
		"",
	}
	tbl := th.NewTable(
		ui.Column{Title: "NAME", Min: 4, Max: 60},
		ui.Column{Title: "STATUS", Min: 10},
		ui.Column{Title: "INSTANCE", Min: 10},
		ui.Column{Title: "AMI", Min: 9},
		ui.Column{Title: "NODES", Min: 7, Align: ui.AlignRight},
	)
	for _, ng := range items {
		tbl.Row(
			th.Paint(pal.White, ng.Name),
			th.Token(render.StatusFromString(ng.Status), ng.Status),
			th.Paint(pal.Text, ng.InstanceType),
			amiToken(th, ng.AMIStatus),
			th.Paint(pal.Text, nodeCountText(ng.ReadyKnown, ng.ReadyNodes, ng.DesiredSize)),
		)
	}
	out = append(out, tbl.Render()...)
	return out
}

// nodeCountText renders the NODES cell honestly: a measured "ready/desired"
// fraction only when readiness was actually measured (--check-readiness against
// a reachable cluster), otherwise just the desired count — never a fabricated
// ready figure. (REF-130)
func nodeCountText(readyKnown bool, ready, desired int32) string {
	if readyKnown {
		return fmt.Sprintf("%d/%d", ready, desired)
	}
	return fmt.Sprintf("%d", desired)
}

// amiToken renders a nodegroup's AMI freshness as a status token.
func amiToken(th *render.Theme, s types.AMIStatus) string {
	switch s {
	case types.AMILatest:
		return th.Token(render.Healthy, s.String())
	case types.AMIOutdated:
		return th.Token(render.Warn, s.String())
	default:
		return th.Token(render.Unknown, s.String())
	}
}
