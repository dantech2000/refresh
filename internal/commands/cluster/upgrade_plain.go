package cluster

import (
	"fmt"
	"io"

	"github.com/dantech2000/refresh/internal/services/upgrade"
	"github.com/dantech2000/refresh/internal/ui"
)

// upgradePlanPlainHeaders is the `cluster upgrade -o plain` header: one row
// per plan step. The human plan is a numbered list without column headers, so
// these names follow the Step fields.
var upgradePlanPlainHeaders = []string{"HOP", "STEP", "TYPE", "TARGET", "VERSION", "STATUS", "DESCRIPTION", "REASON"}

// upgradePlanPlain builds the `cluster upgrade -o plain` plan table. HOP is
// "from→to" in ASCII ("1.30->1.31") and STEP is the 1-based index within the
// hop, matching the human plan's numbering.
func upgradePlanPlain(plan *upgrade.Plan) *ui.PlainTable {
	t := ui.NewPlainTable(upgradePlanPlainHeaders...)
	for _, hop := range plan.Hops {
		for i, s := range hop.Steps {
			t.Row(
				hop.From+"->"+hop.To,
				fmt.Sprintf("%d", i+1),
				string(s.Type),
				s.Target,
				s.Version,
				string(s.Status),
				s.Description,
				s.Reason,
			)
		}
	}
	return t
}

// writeUpgradePlanPlain writes the plan as TSV to out and the plan's summary
// line and warnings to info (stderr), keeping out pure TSV. runUpgrade calls
// it in place of renderPlan when ui.PlainOutput() is true.
func writeUpgradePlanPlain(out, info io.Writer, plan *upgrade.Plan) {
	path := plan.CurrentVersion
	for _, hop := range plan.Hops {
		path += " -> " + hop.To
	}
	_, _ = fmt.Fprintf(info, "upgrade plan: %s %s\n", plan.ClusterName, path)
	for _, w := range plan.Warnings {
		_, _ = fmt.Fprintf(info, "warning: %s\n", w)
	}
	upgradePlanPlain(plan).Write(out)
}
