package cluster

import (
	"fmt"
	"io"
	"strings"
	"unicode"

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
				kebabCase(string(s.Type)),
				s.Target,
				s.Version,
				kebabCase(string(s.Status)),
				s.Description,
				s.Reason,
			)
		}
	}
	return t
}

// kebabCase turns a PascalCase enum value into the lower-case words the
// -o plain columns have always shown: ControlPlane is control-plane, and
// Pending is pending. The JSON value stays PascalCase.
func kebabCase(s string) string {
	var b strings.Builder
	for i, r := range s {
		if unicode.IsUpper(r) {
			if i > 0 {
				b.WriteByte('-')
			}
			r = unicode.ToLower(r)
		}
		b.WriteRune(r)
	}
	return b.String()
}

// writeUpgradePlanPlain writes the plan as TSV to out and the plan's summary
// line and notices to info (stderr), keeping out pure TSV. runUpgrade calls
// it in place of renderPlan when ui.PlainOutput() is true.
func writeUpgradePlanPlain(out, info io.Writer, plan *upgrade.Plan) {
	path := plan.CurrentVersion
	for _, hop := range plan.Hops {
		path += " -> " + hop.To
	}
	_, _ = fmt.Fprintf(info, "upgrade plan: %s %s\n", plan.ClusterName, path)
	for _, n := range plan.Notices {
		_, _ = fmt.Fprintf(info, "notice: %s\n", n)
	}
	upgradePlanPlain(plan).Write(out)
}
