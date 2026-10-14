package view

import (
	"strconv"
	"strings"
	"time"

	"github.com/dantech2000/refresh/internal/apidoc"
)

// changeTimeLayout is how the view of a run prints a time.
const changeTimeLayout = "2006-01-02 15:04:05 UTC"

// DecisionStatus returns the status that draws the decision of a plan.
func DecisionStatus(d apidoc.PlanDecision) Status {
	switch d {
	case apidoc.PlanProceed:
		return Healthy
	case apidoc.PlanAsk:
		return Warn
	case apidoc.PlanBlocked:
		return Fail
	}
	return Neutral
}

// RunStatus returns the status that draws how a run ended.
func RunStatus(s apidoc.RunStatus) Status {
	switch s {
	case apidoc.RunSucceeded, apidoc.RunNothingToDo:
		return Healthy
	case apidoc.RunStarted:
		return Progress
	case apidoc.RunRefused, apidoc.RunBlocked, apidoc.RunFailed, apidoc.RunVerifyFailed:
		return Fail
	}
	return Warn
}

// StepStatus returns the status that draws the result of a step.
func StepStatus(s apidoc.StepStatus) Status {
	switch s {
	case apidoc.StepSucceeded:
		return Healthy
	case apidoc.StepStarted, apidoc.StepInProgress:
		return Progress
	case apidoc.StepFailed, apidoc.StepCancelled, apidoc.StepBlocked:
		return Fail
	case apidoc.StepBusy:
		return Warn
	case apidoc.StepSkipped:
		return Neutral
	}
	return Unknown
}

// PlanView returns the table and plain form of a plan. With several
// clusters, the title of each section ends with the cluster.
func PlanView(doc apidoc.Plan) Description {
	d := Description{Title: "Plan", Subtitle: string(doc.Operation), Failures: doc.Failures}
	worst := apidoc.PlanNothingToDo
	for _, c := range doc.Clusters {
		if decisionRank(c.Decision) > decisionRank(worst) {
			worst = c.Decision
		}
		d.Sections = append(d.Sections, planSections(doc.Operation, c, sectionSuffix(len(doc.Clusters), c.Cluster))...)
	}
	d.Status, d.Label = DecisionStatus(worst), string(worst)
	if worst == apidoc.PlanNothingToDo && planHasBusyTarget(doc) {
		// Nothing would be changed, and not because nothing needs a
		// change: a target is changing already, and the plan is
		// incomplete until that ends.
		d.Status, d.Label = Warn, "TargetBusy"
	}
	if len(doc.SkippedRegions) > 0 {
		d.Sections = append(d.Sections, Section{Title: "SKIPPED REGIONS", Fields: []Field{{Name: "regions", Value: strings.Join(doc.SkippedRegions, ", ")}}})
	}
	return d
}

// planHasBusyTarget reports whether a step of the plan is a target that
// is already changing: the plan leaves it alone, lists it as a failure,
// and exits 4.
func planHasBusyTarget(doc apidoc.Plan) bool {
	for _, c := range doc.Clusters {
		for _, s := range c.Steps {
			if s.Action == apidoc.ActionSkip && s.Reason == apidoc.ReasonInFlight {
				return true
			}
		}
	}
	return false
}

// decisionRank orders the decisions: the worst one stands in the head of
// a plan of several clusters.
func decisionRank(d apidoc.PlanDecision) int {
	switch d {
	case apidoc.PlanBlocked:
		return 3
	case apidoc.PlanAsk:
		return 2
	case apidoc.PlanProceed:
		return 1
	}
	return 0
}

// sectionSuffix returns what follows a section title: nothing for a plan
// of one cluster, and the cluster otherwise.
func sectionSuffix(clusters int, cluster string) string {
	if clusters <= 1 {
		return ""
	}
	return " " + cluster
}

// planSections returns the sections of the plan of one cluster.
func planSections(op apidoc.Operation, c apidoc.ClusterPlan, suffix string) []Section {
	steps := make([]Item, len(c.Steps))
	for i, s := range c.Steps {
		steps[i] = Item{Name: hopStepName(s.Kind, s.Name, s.Hop), Fields: []Field{
			{Name: "kind", Value: string(s.Kind)},
			{Name: "action", Value: string(s.Action)},
			{Name: "reason", Value: string(s.Reason)},
			{Name: "from", Value: stepOrigin(s)},
			{Name: "to", Value: stepTarget(s)},
			{Name: "note", Value: inFlightNote(s.InFlight)},
		}}
	}
	out := []Section{
		{Title: "PLAN" + suffix, Fields: []Field{
			{Name: "operation", Value: string(op)},
			{Name: "cluster", Value: c.Cluster},
			{Name: "region", Value: c.Region},
			{Name: "version", Value: c.Version},
			{Name: "target version", Value: c.TargetVersion},
			{Name: "decision", Value: string(c.Decision), Status: DecisionStatus(c.Decision)},
		}},
		{Title: "STEPS" + suffix, Items: steps},
		{Title: "BLOCKERS" + suffix, Items: blockerItems(c.Blockers)},
	}
	out = append(out, gateSections(c.Gate, suffix)...)
	return append(out, Section{Title: "NOTICES" + suffix, Fields: numbered(c.Notices)})
}

// blockerItems returns one item for each blocker of the plan of a
// cluster. The view of a plan and the view of a run both list them: a
// cluster that the plan blocked has Blocked rows in a run, and the
// blockers say why.
func blockerItems(blockers []apidoc.Blocker) []Item {
	items := make([]Item, len(blockers))
	for i, b := range blockers {
		step := ""
		if b.Step != nil {
			step = hopStepName(b.Step.Kind, b.Step.Name, b.Step.Hop)
		}
		items[i] = Item{Name: strconv.Itoa(i + 1), Fields: []Field{
			{Name: "kind", Value: string(b.Kind), Status: Fail},
			{Name: "step", Value: step},
			{Name: "message", Value: b.Message},
		}}
	}
	return items
}

// gateSections returns the sections of a health gate: the verdict and
// the checks. It returns none for a plan with no gate.
func gateSections(g *apidoc.Gate, suffix string) []Section {
	if g == nil {
		return nil
	}
	status := Healthy
	switch g.Decision {
	case apidoc.GateAsk:
		status = Warn
	case apidoc.GateBlock:
		status = Fail
	}
	return []Section{
		{Title: "HEALTH GATE" + suffix, Fields: []Field{
			{Name: "decision", Value: string(g.Decision), Status: status},
			{Name: "reasons", Value: strings.Join(g.Reasons, "\n")},
			{Name: "accepted", Value: strings.Join(g.Accepted, "\n")},
			{Name: "forced", Value: strings.Join(g.Forced, "\n")},
		}},
		{Title: "CHECKS" + suffix, Items: healthItems(g.Checks)},
	}
}

// healthItems returns one item for each health result.
func healthItems(results []apidoc.HealthResult) []Item {
	items := make([]Item, len(results))
	for i, r := range results {
		items[i] = Item{Name: r.Name, Fields: []Field{
			{Name: "outcome", Value: string(r.Outcome), Status: HealthStatus(r.Outcome)},
			{Name: "message", Value: r.Message},
		}}
	}
	return items
}

// numbered returns the lines as fields named 1, 2, and so on.
func numbered(lines []string) []Field {
	fields := make([]Field, len(lines))
	for i, line := range lines {
		fields[i] = Field{Name: strconv.Itoa(i + 1), Value: line}
	}
	return fields
}

// stepName returns the name of a step in a view: the nodegroup or the
// add-on, and "control-plane" for the step that has no name.
func stepName(kind apidoc.StepKind, name string) string {
	switch {
	case name != "":
		return name
	case kind == apidoc.StepControlPlane:
		return "control-plane"
	}
	return strings.ToLower(string(kind))
}

// hopStepName is stepName for a step of an upgrade, which has the hop of
// its phase after it: "ng-a@1.33". The same nodegroup has a step in each
// hop, so the hop is part of the name of a row. Every other step has no
// hop, and keeps its name.
func hopStepName(kind apidoc.StepKind, name, hop string) string {
	if hop == "" {
		return stepName(kind, name)
	}
	return stepName(kind, name) + "@" + hop
}

// sizesText returns the sizes of a nodegroup as words.
func sizesText(z apidoc.Sizes) string {
	return "desired " + strconv.Itoa(int(z.Desired)) + ", min " + strconv.Itoa(int(z.Min)) + ", max " + strconv.Itoa(int(z.Max))
}

// inFlightNote names the update that makes a target busy, such as
// "VersionUpdate in flight", or returns "" when its type is not known.
func inFlightNote(updateType string) string {
	if updateType == "" {
		return ""
	}
	return updateType + " in flight"
}

// stepOrigin returns where a step starts: the sizes of a scale, or its
// version. A roll adds what its nodes run, each fact with its name, since
// a release version and an image ID are different things:
// "1.32 (release 1.32.0-20250101, AMI ami-0abc)".
func stepOrigin(s apidoc.PlanStep) string {
	if s.Sizes != nil {
		return sizesText(s.Sizes.Before)
	}
	var runs []string
	if s.FromRelease != "" {
		runs = append(runs, "release "+s.FromRelease)
	}
	if s.FromImage != "" {
		runs = append(runs, "AMI "+s.FromImage)
	}
	if len(runs) == 0 {
		return s.From
	}
	return s.From + " (" + strings.Join(runs, ", ") + ")"
}

// stepTarget returns where a step ends: the sizes of a scale, or its
// version, with the image that a roll moves to: "1.32 (AMI ami-0def)".
func stepTarget(s apidoc.PlanStep) string {
	switch {
	case s.Sizes != nil:
		return sizesText(s.Sizes.After)
	case s.ToImage != "":
		return s.To + " (AMI " + s.ToImage + ")"
	}
	return s.To
}

// RunView returns the table and plain form of a run.
func RunView(doc apidoc.Run) Description {
	d := Description{
		Title:    "Run",
		Status:   RunStatus(doc.Status),
		Label:    string(doc.Status),
		Subtitle: string(doc.Operation),
		Failures: doc.Failures,
	}
	for _, c := range doc.Clusters {
		suffix := sectionSuffix(len(doc.Clusters), c.Cluster)
		d.Sections = append(d.Sections,
			Section{Title: "RUN" + suffix, Fields: []Field{
				{Name: "operation", Value: string(doc.Operation)},
				{Name: "cluster", Value: c.Cluster},
				{Name: "region", Value: c.Region},
				{Name: "version", Value: c.Version},
				{Name: "target version", Value: c.TargetVersion},
				{Name: "status", Value: string(c.Status), Status: RunStatus(c.Status)},
			}},
			Section{Title: "RESULTS" + suffix, Items: resultItems(c)},
			Section{Title: "BLOCKERS" + suffix, Items: blockerItems(c.Blockers)},
			Section{Title: "VERIFICATION" + suffix, Items: healthItems(c.Verification)},
		)
		d.Sections = append(d.Sections, gateSections(c.Gate, suffix)...)
		d.Sections = append(d.Sections, Section{Title: "NOTICES" + suffix, Fields: numbered(c.Notices)})
	}
	return d
}

// resultItems returns one item for each step of a cluster, with what the
// plan wanted and what the run did.
func resultItems(c apidoc.ClusterRun) []Item {
	items := make([]Item, len(c.Results))
	for i, r := range c.Results {
		var from, to string
		// The results are in plan order, one for each step.
		if i < len(c.Steps) {
			from, to = stepOrigin(c.Steps[i]), stepTarget(c.Steps[i])
		}
		note, failure := r.Note, ""
		switch {
		case r.InFlight != "":
			note = inFlightNote(r.InFlight)
		case r.Attached:
			note = "attached"
		}
		if r.Failure != nil {
			failure = string(r.Failure.Reason) + ": " + r.Failure.Error
		}
		items[i] = Item{Name: hopStepName(r.Kind, r.Name, r.Hop), Fields: []Field{
			{Name: "kind", Value: string(r.Kind)},
			{Name: "status", Value: string(r.Status), Status: StepStatus(r.Status)},
			{Name: "from", Value: from},
			{Name: "to", Value: to},
			{Name: "update", Value: r.UpdateID},
			{Name: "note", Value: note},
			{Name: "started", Value: changeTime(r.StartedAt)},
			{Name: "finished", Value: changeTime(r.FinishedAt)},
			{Name: "failure", Value: failure},
		}}
	}
	return items
}

// changeTime returns t for the view of a run, or "" for the zero time.
func changeTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(changeTimeLayout)
}

// PlanLines returns the lines that show a plan before a question or a
// run: one for each step that does something, then the steps that are
// left alone, dim.
func PlanLines(th Theme, doc apidoc.Plan) []string {
	var out []string
	for _, c := range doc.Clusters {
		out = append(out, th.Heading("PLAN")+"  "+th.Dim(string(doc.Operation)+", "+c.Cluster+" in "+c.Region))
		width := 0
		for _, s := range c.Steps {
			width = max(width, Width(hopStepName(s.Kind, s.Name, s.Hop)))
		}
		for _, s := range c.Steps {
			line := pad(hopStepName(s.Kind, s.Name, s.Hop), width, false) + columnGap + planStepText(s)
			switch s.Action {
			case apidoc.ActionSubmit, apidoc.ActionAttach, apidoc.ActionVerify:
				out = append(out, indent+line)
			case apidoc.ActionBlocked:
				out = append(out, indent+th.Token(Fail, line))
			default:
				out = append(out, indent+th.Dim(line))
			}
		}
	}
	return out
}

// planStepText says what a plan does with a step, in words.
func planStepText(s apidoc.PlanStep) string {
	reason := ""
	if s.Reason != "" {
		reason = " (" + string(s.Reason) + ")"
	}
	switch s.Action {
	case apidoc.ActionSubmit:
		switch {
		case s.Sizes != nil:
			return "scale from " + stepOrigin(s) + " to " + stepTarget(s) + reason
		case s.Kind == apidoc.StepNodegroup && s.From == s.To:
			// A roll keeps the Kubernetes version and moves the AMI.
			text := "roll onto the latest AMI of " + s.To
			if s.ToImage != "" {
				text += " (" + s.ToImage + ")"
			}
			if s.Force {
				text += ", with --force"
			}
			return text + reason
		case s.From == s.To:
			return "apply " + s.To + " again" + reason
		}
		return "update " + s.From + " to " + stepTarget(s) + reason
	case apidoc.ActionAttach:
		return "wait for the update to " + s.To + " that is running" + reason
	case apidoc.ActionVerify:
		return "verify at " + s.To + reason
	case apidoc.ActionSkip:
		if s.InFlight != "" {
			// The update of someone else that makes the target busy.
			reason = " (" + string(s.Reason) + ": " + s.InFlight + ")"
		}
		return "skip, at " + stepOrigin(s) + reason
	case apidoc.ActionManual:
		return "left to you" + reason
	}
	return "blocked" + reason
}

// StepLine returns the progress line of a step whose result changed, such
// as "vpc-cni: Started (update 0000)".
func StepLine(th Theme, r apidoc.StepResult) string {
	text := hopStepName(r.Kind, r.Name, r.Hop) + ": " + string(r.Status)
	switch {
	case r.Failure != nil:
		text += ": " + r.Failure.Error
	case r.Status == apidoc.StepStarted && r.UpdateID != "":
		text += " (update " + r.UpdateID + ")"
	case r.Attached && r.Status == apidoc.StepInProgress:
		text += " (an update that was already running; not waited for)"
	}
	return th.Token(StepStatus(r.Status), text)
}
