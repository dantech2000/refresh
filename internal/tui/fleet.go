package tui

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/dantech2000/refresh/internal/apidoc"
	"github.com/dantech2000/refresh/internal/fleet"
	"github.com/dantech2000/refresh/internal/tui/state"
	"github.com/dantech2000/refresh/internal/view"
)

// fleetScreen: the cluster table and the selected cluster's card on the
// left, the fleet feed on the right.
func (m Model) fleetScreen(w, h int) Block {
	lw := w * 62 / 100
	rw := w - lw - 1
	return hjoin(h, col{m.fleetTable(lw, h), lw}, vrule(h, m.bd), col{m.fleetFeed(rw, h), rw})
}

type fleetCol struct {
	name string
	w    int
}

// fleetCols sizes the fleet table's columns to the names in the fleet:
// the cluster and region columns fit their longest value (within limits),
// and STATUS takes the rest. Every cell but the last leaves one space
// before the next.
func (m Model) fleetCols(w int) []fleetCol {
	nameW, regionW := len("CLUSTER"), len("REGION")
	for _, c := range m.st.Clusters {
		nameW, regionW = max(nameW, width(c.Name)), max(regionW, width(c.Region))
	}
	nameW, regionW = max(min(nameW, 30), 12)+1, max(min(regionW, 15), 10)+1
	cols := []fleetCol{{"", 2}, {"CLUSTER", nameW}, {"REGION", regionW}, {"VERSION", 13}, {"NODEGROUPS", 14}, {"ADD-ONS", 11}, {"STATUS", 0}}
	const statusMin = 14
	used := func() int {
		n := 2 // the table's indent and right edge
		for _, c := range cols {
			n += c.w
		}
		return n
	}
	// A narrow pane keeps the names readable: REGION goes first, then
	// NODEGROUPS and ADD-ONS (the card below shows them for the selected
	// cluster), and only then does CLUSTER shrink.
	for _, drop := range []string{"REGION", "NODEGROUPS", "ADD-ONS"} {
		if used()+statusMin <= w {
			break
		}
		cols = slices.DeleteFunc(cols, func(c fleetCol) bool { return c.name == drop })
	}
	if over := used() + statusMin - w; over > 0 {
		cols[1].w = max(12, cols[1].w-over)
	}
	cols[len(cols)-1].w = max(10, w-used())
	return cols
}

func (m Model) fleetTable(w, h int) Block {
	need, changing := 0, 0
	for _, c := range m.st.Clusters {
		switch c.Status {
		case view.Warn, view.Fail:
			need++
		case view.Progress:
			changing++
		}
	}
	out := Block{
		{},
		{sp(1), m.heading("Fleet"), sp(2), dim(fmt.Sprintf("%s · %d need attention · %d changing", plural(len(m.st.Clusters), "cluster"), need, changing))},
	}
	cols := m.fleetCols(w)
	head := Line{sp(1)}
	for _, c := range cols {
		head = append(head, Seg{Text: padRight(c.name, c.w), Role: view.RoleDim, Bold: true})
	}
	out = append(out, head, append(Line{sp(1)}, hrule(w-2, m.bd)...))
	if len(m.st.Clusters) == 0 {
		out = append(out, Line{})
		switch {
		case m.st.Sweep.Error != "":
			// No region answered: say why, not "no clusters".
			for i, para := range strings.Split(m.st.Sweep.Error, "\n") {
				r := view.RoleDim
				if i == 0 {
					r = view.RoleFail
				}
				for _, l := range wrap(para, w-4) {
					out = append(out, Line{sp(2), role(r, l)})
				}
			}
		case m.st.Sweep.EndedAt.IsZero():
			// The first sweep has not ended: nothing is known yet.
			out = append(out, Line{sp(2), m.tok(view.Progress, "Sweeping the regions for EKS clusters")},
				Line{sp(2), dim("A large fleet, or many regions, takes a few seconds.")})
		default:
			out = append(out, Line{sp(2), tx("No EKS clusters in the regions swept.")},
				Line{sp(2), dim("The header counts the regions swept; pick others with -r, or all with -A.")})
		}
		return out.fit(w, h)
	}
	// The rows that fit above the selected cluster's card; the window
	// keeps the selection in view. A pane too short for five rows and the
	// card drops the card.
	card := m.clusterCard(w - 2)
	rowsH := h - len(out) - 1 - len(card)
	if rowsH < 5 {
		card, rowsH = nil, h-len(out)
	}
	first, last := 0, len(m.st.Clusters)
	if last > rowsH {
		window := max(1, rowsH-1) // one line says what is above and below
		first = min(max(0, m.sel-window/2), last-window)
		last = first + window
	}
	for i := first; i < last; i++ {
		c := m.st.Clusters[i]
		row := Line{sp(1)}
		for j, fc := range cols {
			cell := m.fleetCell(fc.name, c)
			if j > 0 && j < len(cols)-1 {
				row = append(row, append(cell.Fit(fc.w-1), sp(1))...)
				continue
			}
			row = append(row, cell.Fit(fc.w)...)
		}
		if i == m.sel {
			row[1] = role(view.RoleAccent, m.bd.Marker+" ")
			row = row.Fit(w).WithBG(view.RoleSurface)
		}
		out = append(out, row)
	}
	if first > 0 || last < len(m.st.Clusters) {
		var more []string
		if first > 0 {
			more = append(more, fmt.Sprintf("%d above", first))
		}
		if n := len(m.st.Clusters) - last; n > 0 {
			more = append(more, fmt.Sprintf("%d below", n))
		}
		out = append(out, Line{sp(3), dim(strings.Join(more, " · "))})
	}
	if card != nil {
		out = append(out, Line{})
		out = append(out, card.indent(1)...)
	}
	return out.fit(w, h)
}

func (m Model) fleetCell(column string, c state.Cluster) Line {
	switch column {
	case "":
		return Line{sp(2)}
	case "CLUSTER":
		return Line{tx(c.Name)}
	case "REGION":
		return Line{sub(c.Region)}
	case "VERSION":
		if c.Snapshot == nil || c.Snapshot.Have.Cluster != fleet.Collected {
			return Line{dim("?")}
		}
		l := Line{tx(c.Version.String())}
		switch {
		case c.Support == apidoc.SupportExtended:
			return append(l, sp(1), statusSeg(view.Fail, "ext"))
		case c.Support == apidoc.SupportUnsupported:
			return append(l, sp(1), statusSeg(view.Fail, "eol"))
		case slices.ContainsFunc(c.InFlight, func(s string) bool { return strings.HasPrefix(s, "control plane") }):
			return append(l, sp(1), m.mark(view.Progress))
		}
		return l
	case "NODEGROUPS":
		if c.Snapshot == nil {
			return Line{dim("?")}
		}
		l := Line{tx(strconv.Itoa(len(c.Snapshot.Nodegroups))), sp(1)}
		switch {
		case slices.ContainsFunc(c.InFlight, func(s string) bool { return strings.HasPrefix(s, "nodegroup ") }):
			return append(l, m.mark(view.Progress))
		case c.Stale.AMIs+c.Stale.Behind > 0:
			return append(l, m.tok(view.Warn, fmt.Sprintf("%d stale", c.Stale.AMIs+c.Stale.Behind)))
		case c.Stale.Unknown > 0:
			// An AMI that could not be read is not current: say so.
			return append(l, dim(fmt.Sprintf("? %d unknown", c.Stale.Unknown)))
		}
		return append(l, m.mark(view.Healthy))
	case "ADD-ONS":
		switch {
		case c.Snapshot == nil:
			return Line{dim("?")}
		case slices.ContainsFunc(c.InFlight, func(s string) bool { return strings.HasPrefix(s, "add-on ") }):
			return Line{m.tok(view.Progress, "updating")}
		case c.Stale.Addons > 0:
			return Line{m.tok(view.Warn, fmt.Sprintf("%d stale", c.Stale.Addons))}
		}
		return Line{m.tok(view.Healthy, "current")}
	default:
		return Line{m.tok(c.Status, statusText(c))}
	}
}

// statusText is the one phrase of the STATUS column.
func statusText(c state.Cluster) string {
	switch c.Status {
	case view.Fail:
		switch c.Support {
		case apidoc.SupportExtended:
			return "extended support"
		case apidoc.SupportUnsupported:
			return "unsupported"
		default:
			return "issues"
		}
	case view.Progress:
		return c.InFlight[0]
	case view.Warn:
		return "needs attention"
	case view.Unknown:
		if c.Incomplete {
			return "incomplete"
		}
		return "unknown"
	}
	return "current"
}

// clusterCard summarizes the selected cluster: the version and support,
// the readiness, and what is stale.
func (m Model) clusterCard(w int) Block {
	c := m.cluster()
	if c.Name == "" {
		return nil
	}
	snap := c.Snapshot
	var body Block
	if snap != nil && snap.Cluster.ARN != "" {
		body = append(body, Line{dim(snap.Cluster.ARN)})
	} else {
		body = append(body, Line{dim(c.Region)})
	}
	if c.Incomplete {
		body = append(body, Line{m.tok(view.Warn, "part of this cluster could not be read · counts may be low")})
	}
	line := Line{sub("version "), tx(c.Version.String())}
	if !c.Version.IsZero() {
		line = append(line, dim(" · next hop "+nextMinor(c.Version).String()))
	}
	line = append(line, sp(3), sub("readiness "))
	line = append(line, m.readinessVerdict(c.Readiness)...)
	line = append(line, sp(3), sub("support "))
	line = append(line, m.supportSeg(c)...)
	body = append(body, line)
	// One stale item per line, names aligned: wrapping them as one line
	// split a name from its version.
	type staleItem struct{ name, what string }
	var items []staleItem
	if snap != nil {
		for _, n := range snap.Nodegroups {
			switch status, _ := n.AMIStatus(); {
			case n.State != fleet.Collected:
				continue
			case behindCP(snap, n):
				items = append(items, staleItem{n.Name, "on " + n.Version.String()})
			case status == apidoc.AMIOutdated:
				items = append(items, staleItem{n.Name, "AMI " + orDash(n.AMI.Latest)})
			}
		}
		for _, a := range snap.Addons {
			if behind, _ := a.Behind(); behind {
				items = append(items, staleItem{a.Name, a.Version + " → " + a.Latest})
			}
		}
	}
	if len(items) == 0 && !c.Incomplete {
		body = append(body, Line{m.tok(view.Healthy, "every nodegroup and add-on is current")})
	}
	nameW := 0
	for _, it := range items {
		nameW = max(nameW, width(it.name))
	}
	const staleRows = 8
	for i, it := range items {
		if i == staleRows && len(items) > staleRows+1 {
			body = append(body, Line{dim(fmt.Sprintf("and %d more · enter for the cluster", len(items)-staleRows))})
			break
		}
		body = append(body, Line{sub(padRight(it.name, nameW) + "  "), m.tok(view.Warn, it.what)}.Fit(w-4))
	}
	for _, f := range c.InFlight {
		body = append(body, Line{m.tok(view.Progress, f)})
	}
	body = append(body, Line{chip("r"), sp(1), sub("readiness"), sp(2), chip("enter"), sp(1), sub("open the cluster")})
	return box(Line{bold(view.RoleAccent, c.Name)}, body, w, m.bd)
}

// nextMinor is the version one minor above v: the next hop of an upgrade.
func nextMinor(v fleet.Version) fleet.Version {
	v.Minor++
	return v
}

// behindCP reports whether a nodegroup runs an older minor than the
// control plane.
func behindCP(snap *fleet.Snapshot, n fleet.Nodegroup) bool {
	behind, known := snap.VersionBehind(n)
	return known && behind
}

// supportSeg says where the cluster stands in its support window.
func (m Model) supportSeg(c state.Cluster) Line {
	if c.Snapshot == nil || c.Snapshot.Have.Support != fleet.Collected {
		return Line{dim("unknown")}
	}
	s := c.Snapshot.Cluster.Support
	switch c.Support {
	case apidoc.SupportExtended:
		return Line{role(view.RoleFail, "extended, ends "+day(s.ExtendedEnd))}
	case apidoc.SupportUnsupported:
		return Line{role(view.RoleFail, "ended "+day(s.ExtendedEnd))}
	case apidoc.SupportStandard:
		return Line{tx("standard until " + day(s.StandardEnd))}
	default:
		return Line{dim("unknown")}
	}
}

// day formats a date, or "?" for a date that is not known.
func day(t time.Time) string {
	if t.IsZero() {
		return "?"
	}
	return t.Format("2006-01-02")
}

// readinessVerdict is the one-token verdict of a readiness run.
func (m Model) readinessVerdict(r *state.Readiness) Line {
	switch {
	case r == nil:
		return Line{dim("not run · press r")}
	case r.Running():
		return Line{m.tok(view.Progress, "checking "+dur(m.now.Sub(r.StartedAt)))}
	case r.Err != "":
		return Line{m.tok(view.Fail, "could not read the cluster")}
	}
	return Line{m.tok(readinessStatus(r.Report.Readiness), strings.ToLower(string(r.Report.Readiness)))}
}

// readinessStatus maps a verdict to the status that paints it.
func readinessStatus(r apidoc.Readiness) view.Status {
	switch r {
	case apidoc.ReadinessReady:
		return view.Healthy
	case apidoc.ReadinessReview:
		return view.Warn
	case apidoc.ReadinessBlocked:
		return view.Fail
	}
	return view.Unknown
}

// fleetFeed is the fleet feed, newest first.
func (m Model) fleetFeed(w, h int) Block {
	// With no cluster to scope to, the feed shows everything.
	all := m.feedAll || len(m.st.Clusters) == 0
	scope := "all clusters"
	if !all {
		scope = m.cluster().Name
	}
	out := Block{{}, joinRight(Line{sp(1), m.heading("Feed")}, Line{dim(scope), sp(1), chip("f"), sp(1)}, w)}
	nameW := 0
	for _, c := range m.st.Clusters {
		nameW = max(nameW, width(c.Name))
	}
	sel := m.cluster().Key
	shown := 0
	for i := len(m.st.Feed) - 1; i >= 0 && shown < h-len(out); i-- {
		e := m.st.Feed[i]
		if !all && e.Cluster != sel && e.Cluster.Name != "" {
			continue
		}
		shown++
		l := Line{sp(1), dim(clock(e.At)), sp(1)}
		if all && e.Cluster.Name != "" {
			l = append(l, role(view.RoleInfo, padRight(e.Cluster.Name, nameW)), sp(1))
		}
		l = append(l, m.mark(e.Status), sp(1), sub(e.Text))
		out = append(out, l)
	}
	if shown == 0 {
		out = append(out, Line{sp(1), dim("no events yet")})
	}
	return out.fit(w, h)
}
