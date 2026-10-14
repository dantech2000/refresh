package change

import (
	"fmt"
	"slices"
	"strings"

	"github.com/dantech2000/refresh/internal/health"
)

// unanswered is why a run that needs an answer to a question of kind k
// is refused when nobody can be asked.
func unanswered(k QuestionKind) string {
	switch k {
	case PlanChanged:
		return "changes of the plan need an answer and nobody can be asked; pass --yes"
	case ConfirmStart:
		return "nobody confirmed the change and nobody can be asked; pass --yes"
	}
	return "health warnings need an answer and nobody can be asked; pass --yes or --require-healthy"
}

// question returns the question that a plan needs before it runs, or nil.
// --yes answers every question. A plan that is not confirmed yet asks
// once: about the warnings of the gate when it has some, and else to
// confirm the start. It asks also when nobody can be asked, and next then
// refuses the run: no answer is not a yes. A confirmed plan asks again
// only about what changed: the steps, or a health check that is worse
// than it was.
func (r *runner) question(p *Plan, diff []Change, confirmed bool) *Question {
	if r.common.Policy.Yes {
		return nil
	}
	if !confirmed && p.fleet() {
		return fleetQuestion(p)
	}
	if !confirmed && !p.Kind.independent() {
		return r.engineQuestion(p)
	}
	if !confirmed {
		for i := range p.Clusters {
			if v := p.Clusters[i].Gate; v.Decision == health.Ask {
				return &Question{Kind: GateWarnings, Text: capitalize(v.Question()), Reasons: reasonStrings(v.Reasons)}
			}
		}
		return &Question{Kind: ConfirmStart, Text: startQuestion(p)}
	}
	var changed, worse []string
	for _, c := range diff {
		switch {
		case c.Kind == GateWorse:
			worse = append(worse, c.Message)
		case c.Effect() == AskAgain:
			changed = append(changed, c.Message)
		}
	}
	switch {
	case len(changed) > 0:
		reasons := slices.Concat(changed, worse)
		return &Question{Kind: PlanChanged, Text: "The plan changed (" + strings.Join(reasons, "; ") + "). Continue?", Reasons: reasons}
	case len(worse) > 0:
		return &Question{Kind: GateWarnings, Text: "The health checks got worse (" + strings.Join(worse, "; ") + "). Continue?", Reasons: worse}
	}
	return nil
}

// messages joins the messages of the changes with the effect.
func messages(diff []Change, effect Effect) string {
	var parts []string
	for _, c := range diff {
		if c.Effect() == effect {
			parts = append(parts, c.Message)
		}
	}
	return strings.Join(parts, "; ")
}

func reasonStrings(reasons []health.Reason) []string {
	out := make([]string, len(reasons))
	for i, r := range reasons {
		out[i] = r.String()
	}
	return out
}

// capitalize returns s with an upper-case first letter.
func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// startQuestion is the question that confirms the start of a plan, such
// as "Update add-on vpc-cni in prod from v1.18.0 to v1.19.0?".
func startQuestion(p *Plan) string {
	var steps []Step
	cluster := ""
	for i := range p.Clusters {
		cluster = p.Clusters[i].Cluster
		for _, s := range p.Clusters[i].Steps() {
			if s.work() {
				steps = append(steps, s)
			}
		}
	}
	verb, noun := "Change", "target"
	switch p.Kind {
	case AddonUpdate:
		verb, noun = "Update", "add-on"
	case NodegroupUpdate:
		verb, noun = "Roll", "nodegroup"
	}
	if len(steps) == 1 {
		s := steps[0]
		switch {
		case p.Kind == NodegroupUpdate:
			return fmt.Sprintf("Roll nodegroup %s in %s onto the latest AMI of Kubernetes %s?", s.Ref.Name, cluster, s.To)
		case s.Sizes != nil:
			return fmt.Sprintf("Scale nodegroup %s in %s from %s to %s?", s.Ref.Name, cluster, s.Sizes.Before, s.Sizes.After)
		case s.Action == Attach:
			return fmt.Sprintf("Wait for the update of %s %s in %s to %s?", noun, s.Ref.Name, cluster, s.To)
		case s.From == s.To:
			return fmt.Sprintf("Apply %s %s in %s again at %s?", noun, s.Ref.Name, cluster, s.To)
		}
		return fmt.Sprintf("%s %s %s in %s from %s to %s?", verb, noun, s.Ref.Name, cluster, s.From, s.To)
	}
	names := make([]string, len(steps))
	for i, s := range steps {
		names[i] = s.Ref.Name
	}
	return fmt.Sprintf("%s %d %ss in %s (%s)?", verb, len(steps), noun, cluster, strings.Join(names, ", "))
}

// fleetQuestion is the one question of a fleet roll: it names the
// nodegroups that would be rolled in each cluster, and the clusters that
// are left out. The plan above it has the decision of every cluster. When
// the gate of a cluster has warnings, the question is about them too, and
// a yes accepts them.
func fleetQuestion(p *Plan) *Question {
	q := &Question{Kind: ConfirmStart}
	var rolls, left []string
	count := 0
	for i := range p.Clusters {
		cp := &p.Clusters[i]
		var names []string
		for _, s := range cp.Steps() {
			if s.work() {
				names = append(names, s.Ref.Name)
			}
		}
		switch {
		case cp.Decision() == DecisionBlocked:
			left = append(left, cp.Cluster)
			continue
		case len(names) == 0:
			continue
		}
		count += len(names)
		rolls = append(rolls, cp.Cluster+": "+strings.Join(names, ", "))
		if v := cp.Gate; v.Decision == health.Ask {
			q.Kind = GateWarnings
			for _, reason := range v.Reasons {
				q.Reasons = append(q.Reasons, cp.Cluster+": "+reason.String())
			}
		}
	}
	q.Text = fmt.Sprintf("Roll %d nodegroup(s) in %d cluster(s) (%s)", count, len(rolls), strings.Join(rolls, "; "))
	if len(q.Reasons) > 0 {
		q.Text += despiteWarnings(q.Reasons)
	}
	if len(left) > 0 {
		q.Text += ", and leave out the blocked " + strings.Join(left, ", ")
	}
	q.Text += "?"
	return q
}

// despiteWarnings is the end of a question whose yes accepts the warnings
// of a gate: ", despite the health warnings (...)". The engine question
// ends the same way.
func despiteWarnings(reasons []string) string {
	return ", despite the health warnings (" + strings.Join(reasons, "; ") + ")"
}
