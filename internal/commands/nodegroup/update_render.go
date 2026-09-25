package nodegroup

import "github.com/dantech2000/refresh/internal/render"

// verificationLines builds the human post-roll verification block (pure, so
// tests can check it without a terminal): a verdict line, then one token per
// issue (Fail), per passed check (Healthy), and per check that did not run
// (Neutral, dimmed), so a skipped check never reads as passed.
func verificationLines(th *render.Theme, v PostRollVerification) []string {
	out := make([]string, 0, len(v.Issues)+len(v.Checks)+1)
	if v.OK() {
		out = append(out, th.Line(render.Healthy, "Post-roll verification passed:"))
	} else {
		out = append(out, th.Line(render.Fail, "Post-roll verification found issues:"))
		for _, issue := range v.Issues {
			out = append(out, "  "+th.Token(render.Fail, issue))
		}
	}
	for _, c := range v.Checks {
		if v.Skipped(c) {
			out = append(out, "  "+th.Token(render.Neutral, th.Paint(th.Pal.Dim, c)))
			continue
		}
		out = append(out, "  "+th.Token(render.Healthy, c))
	}
	return out
}
