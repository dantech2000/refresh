package sim

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/dantech2000/refresh/internal/tui/state"
)

// Upgrade phases, in order. As in `cluster upgrade`, the nodegroups roll
// before the add-ons update (the simulator does not model add-ons the new
// control plane cannot run).
const (
	phasePreflight = iota
	phaseControlPlane
	phaseNodegroups
	phaseAddons
	phaseVerify
)

const (
	controlPlaneLo, controlPlaneHi = 8 * time.Minute, 10 * time.Minute
	controlPlanePoll               = 30 * time.Second
	verifyFor                      = 20 * time.Second
)

type upgrade struct {
	w     *World
	c     *cluster
	st    state.Upgrade
	phase int

	cpStart, cpDue, cpNext time.Time
	cpID                   string
	ngs                    []string
	ngIdx                  int
	roll                   *roll
	verifyDue              time.Time
}

func (w *World) planUpgrade(c *cluster) (state.Plan, error) {
	to := state.NextMinor(c.Version)
	p := state.Plan{
		Action:  state.Action{Kind: state.ActionUpgrade, Cluster: c.Name},
		Title:   "Upgrade cluster · " + c.Name + " " + c.Version + " → " + to,
		Command: "refresh cluster upgrade -c " + c.Name + " --to " + to,
	}
	if c.Version == c.Latest {
		p.Title = "Upgrade cluster · " + c.Name
		p.Blocked = c.Name + " already runs " + c.Latest + ", the newest version"
		return p, nil
	}
	p.Changes = append(p.Changes, state.Change{Field: "control plane", From: c.Version, To: to})
	for _, a := range c.Addons {
		if t := addonLatest(a.Name, to); t != "" && t != a.Version {
			p.Changes = append(p.Changes, state.Change{Field: a.Name, From: a.Version, To: t})
		}
	}
	nodes := 0
	for _, ng := range c.Nodegroups {
		nodes += ng.Nodes
		p.Changes = append(p.Changes, state.Change{Field: ng.Name, From: ng.Version, To: to})
	}
	est := 9*time.Minute + time.Duration(len(c.Addons))*50*time.Second + time.Duration(nodes)*perNodeEstimate
	p.Facts = []state.Fact{
		{Key: "phases", Value: "pre-flight → control plane → nodegroups → add-ons → verify"},
		{Key: "nodes", Value: strconv.Itoa(nodes) + " replaced", Note: "one nodegroup at a time"},
		{Key: "estimate", Value: "~" + roundMinutes(est)},
	}
	checks := w.evaluate(c)
	var blockers []string
	passed := 0
	for _, ch := range checks {
		switch ch.Status {
		case state.CheckFail:
			blockers = append(blockers, ch.Name)
			p.Gates = append(p.Gates, state.PlanGate{Status: ch.Status, Text: ch.Name, Note: ch.Summary})
		case state.CheckWarn:
			p.Gates = append(p.Gates, state.PlanGate{Status: ch.Status, Text: ch.Name, Note: ch.Summary})
		default:
			passed++
		}
	}
	p.Gates = append([]state.PlanGate{{Status: state.CheckPass, Text: strconv.Itoa(passed) + " readiness checks pass"}}, p.Gates...)
	switch {
	case c.Busy != "":
		p.Blocked = c.Name + " is busy: " + c.Busy
	case len(blockers) > 0:
		p.Blocked = plural(len(blockers), "readiness blocker") + ": " + strings.Join(blockers, ", ")
	}
	return p, nil
}

// startUpgrade begins an upgrade of name to the next minor. The caller holds
// w.mu, and has checked the plan (the warmup skips the check).
func (w *World) startUpgrade(name string) error {
	c := w.cluster(name)
	if c == nil {
		return fmt.Errorf("cluster %q not found", name)
	}
	u := &upgrade{w: w, c: c}
	u.st = state.Upgrade{Cluster: name, From: c.Version, To: state.NextMinor(c.Version), StartedAt: w.now}
	for _, ph := range []struct {
		name   string
		weight float64
	}{{"Pre-flight", 0.05}, {"Control plane", 0.35}, {"Nodegroups", 0.40}, {"Add-ons", 0.15}, {"Verify", 0.05}} {
		u.st.Phases = append(u.st.Phases, state.Phase{Name: ph.name, Weight: ph.weight})
	}
	c.Busy = "upgrading"
	u.event(state.LevelInfo, "plan", fmt.Sprintf("%s → %s · %d add-ons · %d nodegroups", u.st.From, u.st.To, len(c.Addons), len(c.Nodegroups)), "")
	w.emit(state.Event{Cluster: name, Source: state.SourceUpgrade, Level: state.LevelProgress, Subject: "upgrade", Text: "started " + u.st.From + " → " + u.st.To})
	w.upgrades = append(w.upgrades, u)
	return nil
}

func (u *upgrade) event(lvl state.Level, subject, text, detail string) {
	e := u.w.stamp(state.Event{Cluster: u.c.Name, Source: state.SourceUpgrade, Level: lvl, Subject: subject, Text: text, Detail: detail})
	u.st.Events = appendCapped(u.st.Events, e, rollEventCap)
}

func (u *upgrade) cur() *state.Phase { return &u.st.Phases[u.phase] }

func (u *upgrade) step() {
	if !u.st.Running() {
		return
	}
	p := u.cur()
	if p.Status == state.PhasePending {
		switch {
		case u.st.StopAfter:
			u.stop("stopped before " + strings.ToLower(p.Name))
		case u.st.Paused:
			p.Summary = "paused · press P to go on"
		default:
			u.begin()
		}
		return
	}
	switch u.phase {
	case phaseControlPlane:
		u.stepControlPlane()
	case phaseNodegroups:
		u.stepNodegroups()
	case phaseVerify:
		u.stepVerify()
	}
}

func (u *upgrade) begin() {
	p := u.cur()
	p.Status = state.PhaseRunning
	p.StartedAt = u.w.now
	p.Summary = ""
	u.c.Busy = "upgrading · " + strings.ToLower(p.Name)
	switch u.phase {
	case phasePreflight:
		u.event(state.LevelProgress, "pre-flight", "running readiness checks", "")
		u.w.newReadiness(u.c, func(b, wn int) {
			if b > 0 {
				u.fail("pre-flight found " + plural(b, "blocker"))
				return
			}
			r := u.w.readiness[u.c.Name].st
			u.complete(fmt.Sprintf("%d checks passed · %s accepted", r.Passed(), plural(wn, "warning")))
		})
	case phaseControlPlane:
		u.cpID = u.w.id()
		u.cpStart = u.w.now
		u.cpDue = u.w.now.Add(u.w.between(controlPlaneLo, controlPlaneHi))
		u.cpNext = u.w.now.Add(controlPlanePoll)
		u.w.api(u.c.Name, "UpdateClusterVersion", u.st.To+" · token="+u.cpID)
		u.event(state.LevelProgress, "control", "UpdateClusterVersion sent", "token="+u.cpID[:4]+"…")
		p.Summary = "UpdateClusterVersion · InProgress"
	case phaseAddons:
		stale := u.c.StaleAddons()
		if len(stale) == 0 {
			u.complete("every add-on already current")
			return
		}
		for _, a := range stale {
			p.Items = append(p.Items, state.PhaseItem{Name: a.Name, Text: a.Version + " → " + a.Latest})
		}
		run := u.w.startAddons(u.c, func() { u.complete("every add-on ACTIVE") }, u.c.Name)
		run.onEvent = func(e state.Event) { u.event(e.Level, "add-ons", e.Subject+" "+e.Text, "") }
		run.progress = func(name string, st state.PhaseStatus, text string) {
			for i := range p.Items {
				if p.Items[i].Name == name {
					p.Items[i].Status = st
					p.Items[i].Text = text
					if st == state.PhaseDone {
						p.Items[i].Progress = 1
					}
				}
			}
			done := 0
			for _, it := range p.Items {
				if it.Status == state.PhaseDone {
					done++
				}
			}
			p.Progress = float64(done) / float64(len(p.Items))
		}
	case phaseNodegroups:
		for _, ng := range u.c.Nodegroups {
			if ng.NeedsPatch(u.c.Version) {
				u.ngs = append(u.ngs, ng.Name)
				p.Items = append(p.Items, state.PhaseItem{Name: ng.Name, Text: "queued"})
			}
		}
		if len(u.ngs) == 0 {
			u.complete("every nodegroup already current")
			return
		}
		u.event(state.LevelProgress, "nodegroups", fmt.Sprintf("starting %d rolls · one at a time", len(u.ngs)), "")
		u.startNextRoll()
	case phaseVerify:
		u.verifyDue = u.w.now.Add(verifyFor)
		for _, name := range []string{"nodes Ready", "pods Running", "add-on health"} {
			p.Items = append(p.Items, state.PhaseItem{Name: name, Status: state.PhaseRunning})
		}
		u.event(state.LevelProgress, "verify", "checking nodes, pods, add-on health", "")
	}
}

func (u *upgrade) stepControlPlane() {
	now := u.w.now
	p := u.cur()
	total := u.cpDue.Sub(u.cpStart)
	p.Progress = float64(now.Sub(u.cpStart)) / float64(total)
	if now.Before(u.cpDue) {
		if !now.Before(u.cpNext) {
			u.cpNext = now.Add(controlPlanePoll)
			u.w.api(u.c.Name, "DescribeUpdate", "id="+u.cpID+" status=InProgress")
			if now.Sub(u.cpStart) < controlPlanePoll+time.Second {
				u.event(state.LevelProgress, "control", "update InProgress", "(typical 8–10m)")
			}
		}
		return
	}
	u.c.Version = u.st.To
	u.c.refreshSupport(u.w.epoch, now)
	for i := range u.c.Addons {
		u.c.Addons[i].Latest = addonLatest(u.c.Addons[i].Name, u.st.To)
	}
	u.w.api(u.c.Name, "DescribeUpdate", "id="+u.cpID+" status=Successful")
	u.event(state.LevelOK, "control", "cluster ACTIVE on "+u.st.To, "")
	u.w.emit(state.Event{Cluster: u.c.Name, Source: state.SourceUpgrade, Level: state.LevelOK, Subject: "control plane", Text: u.st.To + " ACTIVE"})
	u.complete("UpdateClusterVersion · ACTIVE on " + u.st.To)
}

func (u *upgrade) startNextRoll() {
	p := u.cur()
	name := u.ngs[u.ngIdx]
	p.Items[u.ngIdx].Status = state.PhaseRunning
	r, err := u.w.startRoll(u.c, name, func(ok bool) { u.rollDone(ok) }, u.c.Name)
	if err != nil {
		u.fail(err.Error())
		return
	}
	r.onEvent = func(e state.Event) {
		if e.Subject == name {
			u.event(e.Level, name, e.Text, e.Detail)
			return
		}
		u.event(e.Level, name, e.Subject+" "+e.Text, e.Detail)
	}
	u.roll = r
}

func (u *upgrade) stepNodegroups() {
	p := u.cur()
	if u.roll == nil || u.ngIdx >= len(p.Items) {
		return
	}
	snap := u.roll.snapshot()
	it := &p.Items[u.ngIdx]
	if snap.Planned > 0 {
		it.Progress = float64(snap.Replaced()) / float64(snap.Planned)
	}
	it.Text = fmt.Sprintf("%d/%d", snap.Replaced(), snap.Planned)
	p.Progress = (float64(u.ngIdx) + it.Progress) / float64(len(p.Items))
}

func (u *upgrade) rollDone(ok bool) {
	p := u.cur()
	it := &p.Items[u.ngIdx]
	took := u.roll.st.EndedAt.Sub(u.roll.st.StartedAt).Round(time.Second)
	if !ok {
		it.Status = state.PhaseFailed
		u.fail(it.Name + " roll failed")
		return
	}
	it.Status, it.Progress = state.PhaseDone, 1
	it.Text = fmt.Sprintf("%d/%d  %s", u.roll.st.Planned, u.roll.st.Planned, took)
	u.roll = nil
	u.ngIdx++
	if u.ngIdx >= len(u.ngs) {
		u.complete(fmt.Sprintf("%d nodegroups on %s", len(u.ngs), u.st.To))
		return
	}
	if u.st.StopAfter {
		u.stop("stopped after " + it.Name)
		return
	}
	u.startNextRoll()
}

func (u *upgrade) stepVerify() {
	if u.w.now.Before(u.verifyDue) {
		return
	}
	p := u.cur()
	for i := range p.Items {
		p.Items[i].Status = state.PhaseDone
	}
	u.complete("nodes Ready · pods Running · add-ons healthy")
}

// complete finishes the current phase and moves to the next, or ends the
// upgrade after the last.
func (u *upgrade) complete(summary string) {
	p := u.cur()
	p.Status = state.PhaseDone
	p.EndedAt = u.w.now
	p.Progress = 1
	p.Summary = summary
	u.event(state.LevelOK, strings.ToLower(p.Name), "phase passed", summary)
	u.phase++
	if u.phase < len(u.st.Phases) {
		return
	}
	u.phase = len(u.st.Phases) - 1
	u.st.EndedAt = u.w.now
	u.c.Busy = ""
	took := u.st.EndedAt.Sub(u.st.StartedAt).Round(time.Second)
	u.event(state.LevelOK, "upgrade", "done · "+u.c.Name+" on "+u.st.To, took.String())
	u.w.emit(state.Event{Cluster: u.c.Name, Source: state.SourceUpgrade, Level: state.LevelOK, Subject: "upgrade", Text: "done · " + u.st.To, Detail: took.String()})
}

func (u *upgrade) stop(why string) {
	p := u.cur()
	if p.Status == state.PhaseRunning || p.Status == state.PhasePending {
		p.Status = state.PhaseStopped
		p.Summary = why
	}
	u.st.Stopped = true
	u.st.EndedAt = u.w.now
	u.c.Busy = ""
	u.event(state.LevelWarn, "upgrade", why, "a rerun resumes from live cluster state")
	u.w.emit(state.Event{Cluster: u.c.Name, Source: state.SourceUpgrade, Level: state.LevelWarn, Subject: "upgrade", Text: why})
}

func (u *upgrade) fail(why string) {
	p := u.cur()
	p.Status = state.PhaseFailed
	p.Summary = why
	p.EndedAt = u.w.now
	u.st.Failed = why
	u.st.EndedAt = u.w.now
	u.c.Busy = ""
	u.event(state.LevelError, strings.ToLower(p.Name), why, "")
	u.w.emit(state.Event{Cluster: u.c.Name, Source: state.SourceUpgrade, Level: state.LevelError, Subject: "upgrade", Text: why})
}

func (u *upgrade) snapshot() state.Upgrade {
	st := u.st
	st.Phases = make([]state.Phase, len(u.st.Phases))
	for i, p := range u.st.Phases {
		p.Items = append([]state.PhaseItem(nil), p.Items...)
		st.Phases[i] = p
	}
	st.Events = append([]state.Event(nil), u.st.Events...)
	return st
}
