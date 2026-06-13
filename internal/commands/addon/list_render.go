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
	tbl := th.NewTable(
		ui.Column{Title: "NAME", Min: 4, Max: 24},
		ui.Column{Title: "VERSION", Min: 8},
		ui.Column{Title: "STATUS", Min: 10},
		ui.Column{Title: "HEALTH", Min: 8},
	)
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

func addonHealthToken(th *render.Theme, health string) string {
	if health == "" {
		return th.Paint(th.Pal.Dim, "—")
	}
	return th.Token(render.StatusFromString(health), health)
}
