package tui

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/dantech2000/refresh/internal/apidoc"
	"github.com/dantech2000/refresh/internal/fleet"
	"github.com/dantech2000/refresh/internal/health"
	"github.com/dantech2000/refresh/internal/view"
)

// clusterScreen: the nodegroups and the add-ons of the selected cluster
// on the left, its readiness on the right.
func (m Model) clusterScreen(w, h int) Block {
	c := m.cluster()
	if c.Name == "" {
		return Block{{}, {sp(1), m.heading("Cluster")}, {}, {sp(1), dim("No cluster selected. The fleet screen lists them.")}}
	}
	head := Line{sp(1), m.heading(c.Name), sp(2), sub(c.Region), sp(2), tx(c.Version.String()), sp(2)}
	head = append(head, m.supportSeg(c)...)
	right := Line{m.tok(c.Status, statusText(c)), sp(1)}
	top := Block{{}, joinRight(head, right, w), append(Line{sp(1)}, hrule(w-2, m.bd)...)}
	bodyH := h - len(top)
	lw := w * 48 / 100
	rw := w - lw - 1
	return append(top, hjoin(bodyH, col{m.resources(c.Snapshot, lw, bodyH), lw}, vrule(bodyH, m.bd), col{m.readinessPane(rw, bodyH), rw})...)
}

// resources lists the nodegroups and the add-ons of the snapshot.
func (m Model) resources(snap *fleet.Snapshot, w, h int) Block {
	out := Block{{}, {sp(1), m.heading("Nodegroups"), sp(2), dim(plural(len(snap.Nodegroups), "nodegroup"))}}
	if len(snap.Nodegroups) == 0 {
		out = append(out, Line{sp(1), dim("none")})
	}
	nameW := len("NAME")
	for _, n := range snap.Nodegroups {
		nameW = max(nameW, width(n.Name))
	}
	nameW = min(nameW, 28) + 2
	out = append(out, Line{sp(1), bold(view.RoleDim, padRight("NAME", nameW)+padRight("VERSION", 9)+padRight("STATUS", 10)+padRight("AMI", 10)+"SIZE")})
	for _, n := range snap.Nodegroups {
		l := Line{sp(1)}
		if n.State != fleet.Collected {
			l = append(l, Line{tx(n.Name)}.Fit(nameW)...)
			l = append(l, m.tok(view.Unknown, "could not be read"))
			out = append(out, l.Fit(w))
			continue
		}
		l = append(l, Line{tx(n.Name)}.Fit(nameW)...)
		version := Line{tx(n.Version.String())}
		if behindCP(snap, n) {
			version = Line{statusSeg(view.Warn, n.Version.String())}
		}
		l = append(l, version.Fit(9)...)
		l = append(l, Line{m.tok(view.StatusOf(n.Status), strings.ToLower(n.Status))}.Fit(10)...)
		l = append(l, Line{m.amiSeg(n)}.Fit(10)...)
		sizes := "?"
		if n.SizesKnown {
			sizes = fmt.Sprintf("%d (%d-%d)", n.Desired, n.Min, n.Max)
		}
		l = append(l, sub(sizes))
		out = append(out, l.Fit(w))
	}
	out = append(out, Line{}, Line{sp(1), m.heading("Add-ons"), sp(2), dim(plural(len(snap.Addons), "add-on"))})
	if len(snap.Addons) == 0 {
		out = append(out, Line{sp(1), dim("none")})
	}
	nameW = len("NAME")
	for _, a := range snap.Addons {
		nameW = max(nameW, width(a.Name))
	}
	nameW = min(nameW, 28) + 2
	out = append(out, Line{sp(1), bold(view.RoleDim, padRight("NAME", nameW)+padRight("VERSION", 24)+padRight("STATUS", 10)+"LATEST")})
	for _, a := range snap.Addons {
		l := Line{sp(1)}
		l = append(l, Line{tx(a.Name)}.Fit(nameW)...)
		if a.State != fleet.Collected {
			l = append(l, m.tok(view.Unknown, "could not be read"))
			out = append(out, l.Fit(w))
			continue
		}
		l = append(l, Line{sub(a.Version)}.Fit(24)...)
		l = append(l, Line{m.tok(view.StatusOf(a.Status), strings.ToLower(a.Status))}.Fit(10)...)
		switch behind, known := a.Behind(); {
		case !known:
			l = append(l, dim("unknown"))
		case behind:
			l = append(l, m.tok(view.Warn, a.Latest))
		default:
			l = append(l, m.tok(view.Healthy, "current"))
		}
		out = append(out, l.Fit(w))
	}
	return out.fit(w, h)
}

// amiSeg is the AMI cell of a nodegroup.
func (m Model) amiSeg(n fleet.Nodegroup) Seg {
	status, known := n.AMIStatus()
	if !known {
		return dim("unknown")
	}
	switch status {
	case apidoc.AMILatest:
		return m.tok(view.Healthy, "latest")
	case apidoc.AMIOutdated:
		return m.tok(view.Warn, "outdated")
	case apidoc.AMIUpdating:
		return m.tok(view.Progress, "updating")
	case apidoc.AMICustom:
		return sub("custom")
	}
	return dim("n/a")
}

// checks are the results of the last readiness run of the selected
// cluster, for the cursor of the cluster screen.
func (m Model) checks() []health.Result {
	r := m.cluster().Readiness
	if r == nil || r.Report == nil {
		return nil
	}
	return r.Report.Checks
}

// readinessPane shows the last readiness run of the selected cluster: the
// verdict, the checks with the selected one in full, and the insights
// that are not passing.
func (m Model) readinessPane(w, h int) Block {
	c := m.cluster()
	r := c.Readiness
	head := Line{sp(1), m.heading("Readiness"), sp(2)}
	switch {
	case r == nil:
		return Block{{}, head, {}, {sp(1), dim("No readiness run yet. Press r to run the checks.")}}.fit(w, h)
	case r.Running():
		head = append(head, m.tok(view.Progress, "checking "+dur(m.now.Sub(r.StartedAt))))
		return Block{{}, head, {}, {sp(1), dim("Reading the cluster, its insights, and its control-plane metrics.")}}.fit(w, h)
	case r.Err != "":
		head = append(head, m.tok(view.Fail, "could not read the cluster"))
		out := Block{{}, head, {}}
		for _, l := range wrap(r.Err, w-2) {
			out = append(out, Line{sp(1), role(view.RoleFail, l)})
		}
		return out.fit(w, h)
	}
	rep := r.Report
	head = append(head, m.readinessVerdict(r)...)
	head = append(head, sp(2), dim("checked "+clock(r.EndedAt)))
	out := Block{{}, head.Fit(w)}
	for _, reason := range rep.Reasons {
		for i, l := range wrap(reason, w-4) {
			prefix := sp(3)
			if i == 0 {
				prefix = Seg{Text: " - ", Role: view.RoleSubtext}
			}
			out = append(out, Line{prefix, sub(l)})
		}
	}
	out = append(out, Line{})
	nameW := len("CHECK")
	for _, ch := range rep.Checks {
		nameW = max(nameW, width(ch.Name))
	}
	nameW += 2
	for i, ch := range rep.Checks {
		marker := sp(2)
		if i == m.checkSel {
			marker = role(view.RoleAccent, m.bd.Marker+" ")
		}
		l := Line{sp(1), marker, m.mark(outcomeStatus(ch.Outcome)), sp(1)}
		l = append(l, Line{tx(ch.Name)}.Fit(nameW)...)
		l = append(l, sub(ch.Message))
		l = l.Fit(w)
		if i == m.checkSel {
			l = l.WithBG(view.RoleSurface)
		}
		out = append(out, l)
	}
	if m.checkSel < len(rep.Checks) {
		ch := rep.Checks[m.checkSel]
		out = append(out, Line{})
		for _, l := range wrap(ch.Message, w-4) {
			out = append(out, Line{sp(3), tx(l)})
		}
		for _, d := range ch.Details {
			for _, l := range wrap(d, w-4) {
				out = append(out, Line{sp(3), sub(l)})
			}
		}
		if n := len(ch.Findings); n > 0 {
			out = append(out, Line{sp(3), dim(plural(n, "finding") + ": " + strings.Join(ch.Findings, ", "))}.Fit(w))
		}
	}
	// The insights that are not passing fill the rest.
	var insights []apidoc.Insight
	for _, in := range rep.Insights {
		if in.Status != "PASSING" {
			insights = append(insights, in)
		}
	}
	if len(out) < h-2 {
		out = append(out, Line{}, Line{sp(1), m.heading("Insights"), sp(2), dim(insightCounts(rep))})
		for _, in := range insights {
			if len(out) >= h {
				break
			}
			l := Line{sp(1), m.mark(insightStatus(in.Status)), sp(1)}
			l = append(l, Line{tx(in.Name)}.Fit(min(40, w/2))...)
			l = append(l, sp(1), sub(in.StatusReason))
			out = append(out, l.Fit(w))
		}
		if len(insights) == 0 && rep.InsightsEvaluated {
			out = append(out, Line{sp(1), m.tok(view.Healthy, "every insight is passing")})
		}
	}
	return out.fit(w, h)
}

// insightCounts is the summary of the insights of a report.
func insightCounts(rep *health.ReadinessReport) string {
	if !rep.InsightsEvaluated {
		return "could not be read"
	}
	counts := map[string]int{}
	for _, in := range rep.Insights {
		counts[in.Status]++
	}
	var parts []string
	for _, s := range []string{"ERROR", "WARNING", "PASSING", "UNKNOWN"} {
		if n := counts[s]; n > 0 {
			parts = append(parts, strconv.Itoa(n)+" "+strings.ToLower(s))
		}
	}
	if len(parts) == 0 {
		return "none"
	}
	return strings.Join(parts, ", ")
}

// outcomeStatus maps the outcome of a check to the status that paints it.
func outcomeStatus(o health.Outcome) view.Status {
	switch o {
	case health.Pass:
		return view.Healthy
	case health.Warn:
		return view.Warn
	case health.Fail:
		return view.Fail
	case health.Skipped:
		return view.Neutral
	}
	return view.Unknown
}

// insightStatus maps an insight status to the status that paints it.
func insightStatus(s string) view.Status {
	switch s {
	case "PASSING":
		return view.Healthy
	case "WARNING":
		return view.Warn
	case "ERROR":
		return view.Fail
	}
	return view.Unknown
}
