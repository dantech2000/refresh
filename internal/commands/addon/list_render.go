package addon

import (
	"fmt"

	"github.com/dantech2000/refresh/internal/render"
	"github.com/dantech2000/refresh/internal/services/addons"
	"github.com/dantech2000/refresh/internal/ui"
)

// addonListLines builds the human `addon list` table (pure, golden-testable)
// with tokenized STATUS and HEALTH cells.
func addonListLines(th *render.Theme, cluster string, rows []addons.AddonSummary) []string {
	pal := th.Pal
	out := []string{
		th.Bold(pal.Mauve, "ADD-ONS") + "  " + th.Paint(pal.White, cluster) +
			th.Paint(pal.Dim, fmt.Sprintf(" · %d", len(rows))),
		"",
	}
	tbl := th.NewTable(addonListColumns()...)
	for _, r := range rows {
		tbl.Row(
			th.Paint(pal.White, r.Name),
			th.Paint(pal.Text, r.Version),
			th.Token(render.StatusFromString(r.Status), r.Status),
			addonHealthToken(th, r.Health),
		)
	}
	out = append(out, tbl.Render()...)
	return out
}

// addonListColumns is the `addon list` column set, shared by the human table
// and the `-o plain` header.
func addonListColumns() []ui.Column {
	return []ui.Column{
		{Title: "NAME", Min: 4, Max: 24},
		{Title: "VERSION", Min: 8},
		{Title: "STATUS", Min: 10},
		{Title: "HEALTH", Min: 8},
	}
}

func addonHealthToken(th *render.Theme, health string) string {
	if health == "" {
		return th.Paint(th.Pal.Dim, "—")
	}
	return th.Token(render.StatusFromString(health), health)
}
