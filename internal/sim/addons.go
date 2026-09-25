package sim

import (
	"fmt"
	"strings"
	"time"

	"github.com/dantech2000/refresh/internal/tui/state"
)

const addonLo, addonHi = 35 * time.Second, 60 * time.Second

// addonRun updates add-ons one at a time.
type addonRun struct {
	w       *World
	c       *cluster
	names   []string
	i       int
	due     time.Time
	next    time.Time
	id      string
	parent  string
	ended   bool
	onEvent func(state.Event)
	// progress reports each add-on's state to a parent upgrade.
	progress func(name string, st state.PhaseStatus, text string)
	done     func()
}

func (w *World) planAddons(c *cluster) (state.Plan, error) {
	p := state.Plan{
		Action:  state.Action{Kind: state.ActionAddons, Cluster: c.Name},
		Title:   "Update add-ons · " + c.Name,
		Command: "refresh addon update --all -c " + c.Name,
	}
	stale := c.StaleAddons()
	for _, a := range stale {
		p.Changes = append(p.Changes, state.Change{Field: a.Name, From: a.Version, To: a.Latest})
	}
	p.Facts = []state.Fact{
		{Key: "order", Value: "one at a time", Note: "each waits for ACTIVE"},
		{Key: "estimate", Value: "~" + roundMinutes(time.Duration(len(stale))*50*time.Second)},
	}
	p.Gates = []state.PlanGate{
		{Status: state.CheckPass, Text: "every add-on ACTIVE"},
		{Status: state.CheckPass, Text: "no health issues"},
	}
	switch {
	case c.Busy != "":
		p.Blocked = c.Name + " is busy: " + c.Busy
	case len(stale) == 0:
		p.Blocked = "every add-on is on its newest version"
	}
	return p, nil
}

// startAddons updates every stale add-on of c. The caller holds w.mu.
func (w *World) startAddons(c *cluster, done func(), parent string) *addonRun {
	a := &addonRun{w: w, c: c, done: done, parent: parent}
	for _, ad := range c.StaleAddons() {
		a.names = append(a.names, ad.Name)
	}
	if parent == "" {
		c.Busy = "updating add-ons"
		w.emit(state.Event{Cluster: c.Name, Source: state.SourceAddon, Level: state.LevelProgress, Subject: "add-ons",
			Text: "update started", Detail: strings.Join(a.names, ", ")})
	}
	w.addonRuns = append(w.addonRuns, a)
	return a
}

func (a *addonRun) addon() *state.Addon {
	for i := range a.c.Addons {
		if a.c.Addons[i].Name == a.names[a.i] {
			return &a.c.Addons[i]
		}
	}
	return nil
}

func (a *addonRun) step() {
	if a.ended {
		return
	}
	now := a.w.now
	if a.i >= len(a.names) {
		a.ended = true
		if a.parent == "" {
			a.c.Busy = ""
			a.w.emit(state.Event{Cluster: a.c.Name, Source: state.SourceAddon, Level: state.LevelOK, Subject: "add-ons", Text: "all add-ons current"})
		}
		if a.done != nil {
			a.done()
		}
		return
	}
	ad := a.addon()
	if ad == nil {
		a.i++
		return
	}
	if a.due.IsZero() {
		a.id = a.w.id()
		a.due = now.Add(a.w.between(addonLo, addonHi))
		a.next = now.Add(describeEvery)
		ad.Status = "UPDATING"
		a.w.api(a.c.Name, "UpdateAddon", fmt.Sprintf("%s %s → %s · id=%s", ad.Name, ad.Version, ad.Latest, a.id))
		a.report(ad.Name, state.PhaseRunning, ad.Version+" → "+ad.Latest)
		return
	}
	if now.Before(a.due) {
		if !now.Before(a.next) {
			a.next = now.Add(describeEvery)
			a.w.api(a.c.Name, "DescribeUpdate", "id="+a.id+" status=InProgress")
		}
		return
	}
	ad.Version = ad.Latest
	ad.Status = "ACTIVE"
	a.w.api(a.c.Name, "DescribeUpdate", "id="+a.id+" status=Successful")
	e := state.Event{Cluster: a.c.Name, Source: state.SourceAddon, Level: state.LevelOK, Subject: ad.Name, Text: "ACTIVE " + ad.Version}
	a.w.emit(e)
	if a.onEvent != nil {
		a.onEvent(e)
	}
	a.report(ad.Name, state.PhaseDone, ad.Version)
	a.i++
	a.due = time.Time{}
}

func (a *addonRun) report(name string, st state.PhaseStatus, text string) {
	if a.progress != nil {
		a.progress(name, st, text)
	}
}
