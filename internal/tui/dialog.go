package tui

import (
	"context"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/dantech2000/refresh/internal/apidoc"
	"github.com/dantech2000/refresh/internal/change"
	"github.com/dantech2000/refresh/internal/fleet"
	"github.com/dantech2000/refresh/internal/tui/state"
	"github.com/dantech2000/refresh/internal/view"
)

// picker is a list of choices: the stale nodegroups of a patch, or the
// parts of an upgrade. enter previews the chosen one.
type picker struct {
	title string
	items []pickItem
	sel   int
}

// pickItem is one choice: a name, why it is offered, a note, and the
// action it previews.
type pickItem struct {
	name, why, note string
	status          view.Status
	action          state.Action
}

// planning is a dry run on its way: its action, and the cancel of its
// context, which esc calls.
type planning struct {
	action state.Action
	seq    int
	cancel context.CancelFunc
}

// openDialog returns the dialog on screen, if any.
func (m Model) openDialog() (dialogParts, bool) {
	switch {
	case m.pick != nil:
		return m.pickerParts(dialogWidth(m.w, 96)), true
	case m.confirm != nil:
		return m.confirmParts(dialogWidth(m.w, 116)), true
	case m.help:
		return m.helpParts(dialogWidth(m.w, 100)), true
	}
	return dialogParts{}, false
}

// plan starts the dry run of an action in a command. A newer dry run, a
// closed picker, or esc drops the reply of an older one.
func (m *Model) plan(a state.Action) tea.Cmd {
	if m.planning != nil {
		m.planning.cancel()
	}
	m.planSeq++
	ctx, cancel := context.WithCancel(m.ctx)
	m.planning = &planning{action: a, seq: m.planSeq, cancel: cancel}
	m.focusAfter = 0
	m.notice = ""
	seq, b := m.planSeq, m.b
	return func() tea.Msg {
		p, err := b.Plan(ctx, a)
		return proposalMsg{seq: seq, p: p, err: err}
	}
}

// cancelPlan drops the dry run on its way and says so.
func (m *Model) cancelPlan() {
	if m.planning == nil {
		return
	}
	m.planning.cancel()
	m.say(view.Neutral, "dry run of %s cancelled", describeAction(m.planning.action))
	m.planning = nil
}

// start runs the proposal of the dialog in a command. The dialog stays
// open, with its keys off, until the backend answers.
func (m *Model) start() tea.Cmd {
	id, b, ctx := m.confirm.ID, m.b, m.ctx
	m.starting, m.confirmErr = true, ""
	return func() tea.Msg {
		cid, err := b.Start(ctx, id)
		return startedMsg{id: cid, err: err}
	}
}

// describeAction names an action for a notice.
func describeAction(a state.Action) string {
	switch a.Kind {
	case state.ActionRoll:
		return "the roll of " + a.Nodegroup + " in " + a.Cluster.Name
	case state.ActionAddons:
		return "the add-on update of " + a.Cluster.Name
	case state.ActionUpgrade:
		return "the upgrade of " + a.Cluster.Name + " to " + a.To.String()
	case state.ActionRollback:
		return "the rollback of " + a.Cluster.Name
	}
	return string(a.Kind)
}

// busyReason says why a change cannot be proposed on the cluster, or "".
func (m Model) busyReason() string {
	c := m.cluster()
	switch {
	case c.Change != 0:
		return c.Name + " has a change in this UI; press 3 to watch it"
	case c.Changing():
		return c.Name + " is changing (" + strings.Join(c.InFlight, ", ") + "); wait for it"
	}
	return ""
}

// saidBusy says why nothing can be proposed, and reports whether it did.
func (m *Model) saidBusy() bool {
	if why := m.busyReason(); why != "" {
		m.say(view.Warn, "%s", why)
		return true
	}
	return false
}

// planRoll previews a roll of the selected cluster's stale nodegroup, or
// opens a picker when more than one is stale.
func (m *Model) planRoll() tea.Cmd {
	c := m.cluster()
	items := patchItems(c)
	switch len(items) {
	case 0:
		m.say(view.Healthy, "every nodegroup in %s is current", c.Name)
		return nil
	case 1:
		return m.plan(items[0].action)
	default:
		m.openPicker(&picker{title: "Roll which nodegroup?", items: items})
		return nil
	}
}

// patchItems are the nodegroups p offers: one on an outdated AMI gets a
// roll (nodegroup update); one behind the control plane rolls to its
// version (cluster upgrade --only nodegroups -n).
func patchItems(c state.Cluster) []pickItem {
	var items []pickItem
	if c.Snapshot == nil {
		return nil
	}
	for _, n := range c.Snapshot.Nodegroups {
		if n.State != fleet.Collected {
			continue
		}
		status, _ := n.AMIStatus()
		switch {
		case behindCP(c.Snapshot, n):
			items = append(items, pickItem{name: n.Name, why: n.Version.String() + " → " + c.Version.String(), note: fmt.Sprintf("%d nodes", n.Desired), status: view.Warn,
				action: state.Action{Kind: state.ActionUpgrade, Cluster: c.Key, Nodegroup: n.Name, To: c.Version, Only: []change.Part{change.PartNodegroups}}})
		case status == apidoc.AMIOutdated:
			items = append(items, pickItem{name: n.Name, why: "AMI " + orDash(n.AMI.Current) + " → " + orDash(n.AMI.Latest), note: fmt.Sprintf("%d nodes", n.Desired), status: view.Warn,
				action: state.Action{Kind: state.ActionRoll, Cluster: c.Key, Nodegroup: n.Name}})
		}
	}
	return items
}

// planUpgrade offers the parts of an upgrade of the selected cluster, as
// cluster upgrade --only does: the control plane alone, with its add-ons,
// or everything, to the next minor; and catching the nodegroups and the
// add-ons that lag up to the version the control plane runs.
func (m *Model) planUpgrade() tea.Cmd {
	c := m.cluster()
	next := nextMinor(c.Version)
	up := func(to fleet.Version, only ...change.Part) state.Action {
		return state.Action{Kind: state.ActionUpgrade, Cluster: c.Key, To: to, Only: only}
	}
	hop := c.Version.String() + " → " + next.String()
	items := []pickItem{
		{name: "Control plane only", why: hop, note: "add-ons and nodegroups stay", status: view.Progress, action: up(next, change.PartControlPlane)},
		{name: "Control plane and add-ons", why: hop, note: "nodegroups stay", status: view.Progress, action: up(next, change.PartControlPlane, change.PartAddons)},
		{name: "Everything", why: hop, note: "then the add-ons and the nodegroup rolls", status: view.Progress, action: up(next)},
	}
	if c.Stale.Behind > 0 {
		items = append(items, pickItem{name: "Catch up the nodegroups", why: "roll to " + c.Version.String(), note: plural(c.Stale.Behind, "nodegroup") + " behind", status: view.Warn, action: up(c.Version, change.PartNodegroups)})
	}
	if c.Stale.Addons > 0 {
		items = append(items, pickItem{name: "Catch up the add-ons", why: "newest for " + c.Version.String(), note: plural(c.Stale.Addons, "add-on") + " behind", status: view.Warn, action: up(c.Version, change.PartAddons)})
	}
	m.openPicker(&picker{title: "Upgrade " + c.Name + ": what?", items: items})
	return nil
}

// openPicker shows p: nothing may open or move behind it.
func (m *Model) openPicker(p *picker) {
	m.cancelPlan()
	m.focusAfter = 0
	m.pick, m.scroll = p, 0
}

// pickerBindings apply while the picker is open.
func pickerBindings() []binding {
	return []binding{
		{keys: keysUp, label: "↑↓", desc: "choose", do: func(m *Model) tea.Cmd { m.pick.sel = max(0, m.pick.sel-1); m.scrollTo(m.pick.sel); return nil }},
		{keys: keysDown, do: func(m *Model) tea.Cmd {
			m.pick.sel = min(len(m.pick.items)-1, m.pick.sel+1)
			m.scrollTo(m.pick.sel)
			return nil
		}},
		{keys: keysTop, label: "g G", desc: "first, last", do: func(m *Model) tea.Cmd { m.pick.sel = 0; m.scrollTo(0); return nil }},
		{keys: keysBottom, do: func(m *Model) tea.Cmd {
			m.pick.sel = max(0, len(m.pick.items)-1)
			m.scrollTo(m.pick.sel)
			return nil
		}},
		{keys: []string{"enter"}, label: "enter", desc: "dry-run the choice", do: func(m *Model) tea.Cmd {
			p := m.pick
			m.pick = nil
			if p.sel < len(p.items) {
				return m.plan(p.items[p.sel].action)
			}
			return nil
		}},
		{keys: []string{"esc", "q"}, label: "esc", desc: "close", do: func(m *Model) tea.Cmd { m.pick = nil; return nil }},
	}
}

// scrollTo keeps row visible in the open dialog.
func (m *Model) scrollTo(row int) {
	d, ok := m.openDialog()
	if !ok {
		return
	}
	rows := m.bodyRows(d)
	switch {
	case row < m.scroll:
		m.scroll = row
	case row >= m.scroll+rows:
		m.scroll = row - rows + 1
	}
	m.scroll = m.clampScroll(m.scroll)
}

func (m Model) pickerParts(w int) dialogParts {
	p := m.pick
	head := Block{{bold(view.RoleAccent, p.title)}, {}}
	nameW := 0
	for _, it := range p.items {
		nameW = max(nameW, width(it.name))
	}
	var body Block
	for i, it := range p.items {
		marker := sp(2)
		if i == p.sel {
			marker = role(view.RoleAccent, m.bd.Marker+" ")
		}
		l := Line{marker, m.mark(it.status), sp(1), tx(padRight(it.name, nameW)), sp(2), sub(it.why), sp(2), dim(it.note)}
		if i == p.sel {
			l = l.Fit(w - 4).WithBG(view.RoleSurface)
		}
		body = append(body, l)
	}
	foot := Block{{}, {chip("enter"), sp(1), dim("dry run"), sp(2), chip("esc"), sp(1), dim("close")}}
	return dialogParts{head: head, body: body, foot: foot, w: w}
}

// dialogBindings apply while the proposal dialog is open. y starts the
// change: the one key that changes a cluster.
func dialogBindings() []binding {
	canStart := func(m Model) bool { return m.confirm != nil && !m.confirm.Blocked && !m.starting }
	return []binding{
		{keys: []string{"y"}, label: "y", desc: "start the change", when: canStart, do: func(m *Model) tea.Cmd { return m.start() }},
		{keys: []string{"c"}, label: "c", desc: "copy the command to the clipboard (OSC 52; the terminal decides)", do: func(m *Model) tea.Cmd {
			m.say(view.Neutral, "command sent to the terminal clipboard")
			return tea.SetClipboard(m.confirm.Command)
		}},
		{keys: []string{"esc", "n", "q"}, label: "esc n q", desc: "close (not while the change is starting)",
			when: func(m Model) bool { return !m.starting },
			do:   func(m *Model) tea.Cmd { m.confirm, m.confirmErr = nil, ""; return nil }},
	}
}

// confirmParts is the dry run of an action and the start key.
func (m Model) confirmParts(w int) dialogParts {
	p := m.confirm
	inner := w - 4
	title := kindLabel(actionKind(p.Action.Kind)) + " " + p.Action.Cluster.Name
	head := Block{joinRight(Line{bold(view.RoleAccent, title)}, Line{dim("dry run · nothing changed yet")}, inner), {}}
	var body Block
	for _, cp := range p.Doc.Clusters {
		body = append(body, m.planSteps(cp, inner)...)
		body = append(body, m.planGate(cp, inner)...)
	}
	for _, f := range p.Doc.Failures {
		for _, line := range wrap(string(f.Kind)+" "+f.Name+": "+f.Error, inner-4) {
			body = append(body, Line{sp(2), m.mark(view.Unknown), sp(1), sub(line)})
		}
	}
	body = append(body, Line{})
	const cmdKey = "CLI equivalent  "
	for i, s := range wrap(p.Command, max(10, inner-width(cmdKey))) {
		key := sp(width(cmdKey))
		if i == 0 {
			key = dim(cmdKey)
		}
		body = append(body, Line{key, sub(s)})
	}
	return dialogParts{head: head, body: body, foot: m.confirmFoot(inner), w: w}
}

// planSteps lists the steps of the plan of one cluster.
func (m Model) planSteps(cp apidoc.ClusterPlan, inner int) Block {
	out := Block{{bold(view.RoleDim, "STEPS"), sp(2), m.tok(view.DecisionStatus(cp.Decision), strings.ToLower(string(cp.Decision)))}}
	nameW := 0
	for _, s := range cp.Steps {
		nameW = max(nameW, width(stepDocName(s)))
	}
	for _, s := range cp.Steps {
		l := Line{sp(2), m.mark(actionStatus(s.Action)), sp(1), tx(padRight(stepDocName(s), nameW)), sp(2), sub(strings.ToLower(string(s.Action)))}
		if s.Reason != "" {
			l = append(l, sp(1), dim("("+string(s.Reason)+")"))
		}
		if s.From != "" || s.To != "" {
			l = append(l, sp(2), sub(orDash(s.From)+" → "+orDash(s.To)))
		}
		if s.Phase != "" && s.Phase != apidoc.PhaseRoll && s.Phase != apidoc.PhaseAddons {
			l = append(l, sp(2), dim(strings.TrimSpace(string(s.Phase)+" "+s.Hop)))
		}
		out = append(out, l.Fit(inner))
	}
	return out
}

// planGate shows the health gate, the blockers, and the notices of the
// plan of one cluster.
func (m Model) planGate(cp apidoc.ClusterPlan, inner int) Block {
	var out Block
	if cp.Gate != nil {
		gate := gateDocStatus(cp.Gate.Decision)
		out = append(out, Line{}, Line{bold(view.RoleDim, "HEALTH GATE"), sp(2), m.tok(gate, strings.ToLower(string(cp.Gate.Decision)))})
		for _, r := range cp.Gate.Reasons {
			for _, line := range wrap(r, inner-4) {
				out = append(out, Line{sp(2), m.mark(gate), sp(1), sub(line)})
			}
		}
	}
	if len(cp.Blockers) > 0 {
		out = append(out, Line{}, Line{bold(view.RoleDim, "BLOCKERS")})
		for _, b := range cp.Blockers {
			for _, line := range wrap(b.Message, inner-4) {
				out = append(out, Line{sp(2), m.mark(view.Fail), sp(1), role(view.RoleFail, line)})
			}
		}
	}
	for _, n := range cp.Notices {
		for _, line := range wrap(n, inner-4) {
			out = append(out, Line{sp(2), m.mark(view.Warn), sp(1), sub(line)})
		}
	}
	return out
}

// confirmFoot is the verdict and the keys of the dry run, which stay on
// screen however the body scrolls.
func (m Model) confirmFoot(inner int) Block {
	p := m.confirm
	foot := Block{{}}
	switch {
	case m.confirmErr != "":
		for _, line := range wrap(m.confirmErr, inner-2) {
			foot = append(foot, Line{m.mark(view.Fail), sp(1), role(view.RoleFail, line)})
		}
	case p.Blocked:
		foot = append(foot, Line{m.mark(view.Fail), sp(1), role(view.RoleFail, "a real run would be blocked; nothing can start")})
	}
	switch {
	case m.starting:
		foot = append(foot, Line{m.tok(view.Progress, "starting")})
	case m.st.ReadOnly:
		foot = append(foot, Line{role(view.RoleWarn, "read-only: run the command above, or start the UI with --allow-changes"), sp(2), chip("esc"), sp(1), dim("close")})
	case p.Blocked:
		foot = append(foot, Line{chip("esc"), sp(1), dim("close")})
	default:
		foot = append(foot, Line{chip("y"), sp(1), role(view.RoleFail, "start the change"), sp(2), chip("c"), sp(1), dim("copy the command"), sp(2), chip("esc"), sp(1), dim("close")})
	}
	return foot
}

func stepDocName(s apidoc.PlanStep) string {
	if s.Name == "" {
		return "control plane"
	}
	return s.Name
}

func actionKind(k state.ActionKind) change.Kind {
	switch k {
	case state.ActionRoll:
		return change.NodegroupUpdate
	case state.ActionAddons:
		return change.AddonUpdate
	case state.ActionUpgrade:
		return change.ClusterUpgrade
	case state.ActionRollback:
		return change.ClusterRollback
	}
	return change.Kind(k)
}

func actionStatus(a apidoc.StepAction) view.Status {
	switch a {
	case apidoc.ActionSubmit, apidoc.ActionAttach, apidoc.ActionVerify:
		return view.Progress
	case apidoc.ActionBlocked:
		return view.Fail
	case apidoc.ActionManual:
		return view.Warn
	}
	return view.Neutral
}

func gateDocStatus(d apidoc.GateDecision) view.Status {
	switch d {
	case "Go":
		return view.Healthy
	case "Ask":
		return view.Warn
	case "Block":
		return view.Fail
	}
	return view.Unknown
}
