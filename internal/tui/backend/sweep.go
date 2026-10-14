package backend

import (
	"cmp"
	"context"
	"errors"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/dantech2000/refresh/internal/apidoc"
	"github.com/dantech2000/refresh/internal/fleet"
	"github.com/dantech2000/refresh/internal/tui/state"
	"github.com/dantech2000/refresh/internal/view"
)

// summary is what the feed compares between two sweeps of one cluster.
type summary struct {
	version fleet.Version
	// statuses are the EKS statuses of the control plane and of every
	// nodegroup and add-on, by "nodegroup ng-a" and the like.
	statuses map[string]string
}

// doSweep sweeps the fleet once and publishes it: each region as it
// answers, then the summary, the feed lines, and the readiness windows.
func (b *Live) doSweep() {
	started := b.opts.Now()
	b.mu.Lock()
	b.sweep = state.Sweep{Regions: len(b.opts.Regions.Names), StartedAt: started, Count: b.sweep.Count, Interval: b.opts.Interval}
	b.mu.Unlock()
	b.bump()

	ctx, cancel := b.withTimeout()
	defer cancel()
	fetcher := &fleet.Fetcher{Now: b.opts.Now}
	res := fetcher.Sweep(ctx, b.provider, b.opts.Regions, fleet.SweepOptions{Need: need, OnRegion: b.regionDone})

	var sweepErr error
	if len(res.Answered) == 0 && b.ctx.Err() == nil {
		sweepErr = b.explain(ctx, res)
	}
	b.mu.Lock()
	b.finish(res, sweepErr)
	b.mu.Unlock()
	b.bump()
}

// regionDone takes the snapshots of one region of the sweep in progress
// and publishes the fleet with that region replaced. It is the OnRegion
// function of the sweep, so it must not block.
func (b *Live) regionDone(region string, snaps []*fleet.Snapshot) {
	rows := make([]state.Cluster, 0, len(snaps))
	for _, snap := range snaps {
		rows = append(rows, row(snap))
	}
	b.mu.Lock()
	b.rowsByRegion[region] = rows
	b.sweep.Answered = append(slices.Clone(b.sweep.Answered), region)
	b.clusters = b.sorted()
	b.mu.Unlock()
	b.bump()
}

// sorted returns every row of every region, by region and then by name.
// The caller holds b.mu.
func (b *Live) sorted() []state.Cluster {
	var out []state.Cluster
	for _, rows := range b.rowsByRegion {
		out = append(out, rows...)
	}
	slices.SortFunc(out, func(a, c state.Cluster) int {
		return cmp.Or(cmp.Compare(a.Region, c.Region), cmp.Compare(a.Name, c.Name))
	})
	return out
}

// explain returns why no region answered.
func (b *Live) explain(ctx context.Context, res *fleet.SweepResult) error {
	if b.opts.SweepError != nil {
		return b.opts.SweepError(ctx, res)
	}
	if len(res.Failures) > 0 {
		return errors.New(res.Failures[0].Error)
	}
	return errors.New("every region was skipped: the credentials cannot reach them")
}

// finish records the end of a sweep: the regions that did not answer lose
// their rows, and the feed gets the lines of what changed. The caller
// holds b.mu.
func (b *Live) finish(res *fleet.SweepResult, sweepErr error) {
	for _, f := range res.Failures {
		delete(b.rowsByRegion, f.Name)
	}
	for _, region := range res.Skipped {
		delete(b.rowsByRegion, region)
	}
	b.clusters = b.sorted()
	b.sweep.Answered = res.Answered
	b.sweep.Skipped = res.Skipped
	b.sweep.Failed = res.Failures
	b.sweep.EndedAt = b.opts.Now()
	b.sweep.Count++
	if sweepErr != nil {
		b.sweep.Error = sweepErr.Error()
	}
	for _, f := range res.Failures {
		b.say(state.Key{}, view.Fail, "region "+f.Name+": "+f.Error)
	}
	if len(res.Skipped) > 0 && !slices.Equal(res.Skipped, b.skippedBefore) {
		b.say(state.Key{}, view.Unknown, "skipped "+strconv.Itoa(len(res.Skipped))+" regions closed to the credentials: "+strings.Join(res.Skipped, ", "))
	}
	b.skippedBefore = res.Skipped
	if b.sweep.Count == 1 {
		b.sayFirstSweep()
	} else {
		b.sayChanges()
	}
	b.previous = make(map[state.Key]summary, len(b.clusters))
	for _, c := range b.clusters {
		b.previous[c.Key] = summarize(c)
	}
	b.adopt()
}

// refetchCluster reads one cluster again, after a change on it ended, and
// replaces its row. The whole fleet is not swept for one cluster.
func (b *Live) refetchCluster(key state.Key) {
	ctx, cancel := b.withTimeout()
	defer cancel()
	fetcher := &fleet.Fetcher{Now: b.opts.Now}
	snap, _ := fetcher.Fetch(ctx, b.provider.Clients(key.Region), key.Name, need)
	fresh := row(snap)
	b.mu.Lock()
	rows := b.rowsByRegion[key.Region]
	if i := slices.IndexFunc(rows, func(c state.Cluster) bool { return c.Key == key }); i >= 0 {
		rows = slices.Clone(rows)
		rows[i] = fresh
		b.rowsByRegion[key.Region] = rows
		b.clusters = b.sorted()
		b.previous[key] = summarize(fresh)
	}
	b.mu.Unlock()
	b.bump()
}

// sayFirstSweep writes one summary of the fleet, then one line per
// cluster that has failed. A line per warning would flood the feed of a
// large fleet.
func (b *Live) sayFirstSweep() {
	attention, changing, incomplete := 0, 0, 0
	for _, c := range b.clusters {
		switch c.Status {
		case view.Warn, view.Fail:
			attention++
		case view.Progress:
			changing++
		case view.Unknown:
			incomplete++
		}
	}
	if len(b.clusters) == 0 {
		return
	}
	text := strconv.Itoa(len(b.clusters)) + " clusters: " + strconv.Itoa(attention) + " need attention, " + strconv.Itoa(changing) + " changing"
	if incomplete > 0 {
		text += ", " + strconv.Itoa(incomplete) + " incomplete"
	}
	b.say(state.Key{}, view.Neutral, text)
	for _, c := range b.clusters {
		if c.Status == view.Fail {
			b.say(c.Key, view.Fail, failText(c))
		}
	}
}

// failText says why a row has failed.
func failText(c state.Cluster) string {
	var parts []string
	if c.Support == apidoc.SupportExtended || c.Support == apidoc.SupportUnsupported {
		parts = append(parts, strings.ToLower(string(c.Support))+" support")
	}
	if c.Snapshot != nil {
		if n := len(c.Snapshot.Cluster.HealthIssues); n > 0 {
			parts = append(parts, strconv.Itoa(n)+" control-plane issues")
		}
		for _, n := range c.Snapshot.Nodegroups {
			if view.StatusOf(n.Status) == view.Fail {
				parts = append(parts, "nodegroup "+n.Name+" "+n.Status)
			}
		}
		for _, a := range c.Snapshot.Addons {
			if view.StatusOf(a.Status) == view.Fail {
				parts = append(parts, "add-on "+a.Name+" "+a.Status)
			}
		}
	}
	if len(parts) == 0 {
		return "needs attention"
	}
	return strings.Join(parts, ", ")
}

// sayChanges writes one line per cluster whose version, statuses, or
// listing changed since the sweep before.
func (b *Live) sayChanges() {
	seen := map[state.Key]bool{}
	for _, c := range b.clusters {
		seen[c.Key] = true
		now := summarize(c)
		before, ok := b.previous[c.Key]
		if !ok {
			b.say(c.Key, view.Neutral, "new cluster, "+c.Version.String())
			continue
		}
		if before.version != now.version && !now.version.IsZero() && !before.version.IsZero() {
			b.say(c.Key, view.Neutral, "control plane "+before.version.String()+" -> "+now.version.String())
		}
		for _, name := range slices.Sorted(maps.Keys(now.statuses)) {
			if old, had := before.statuses[name]; had && old != now.statuses[name] {
				status := view.StatusOf(now.statuses[name])
				if status == view.Neutral {
					status = view.Healthy
				}
				b.say(c.Key, status, name+" "+old+" -> "+now.statuses[name])
			}
		}
	}
	for _, key := range slices.SortedFunc(maps.Keys(b.previous), func(a, c state.Key) int {
		return cmp.Or(cmp.Compare(a.Region, c.Region), cmp.Compare(a.Name, c.Name))
	}) {
		if !seen[key] {
			b.say(key, view.Unknown, "no longer listed")
		}
	}
}

// summarize returns what the feed compares of a row.
func summarize(c state.Cluster) summary {
	s := summary{version: c.Version, statuses: map[string]string{}}
	if c.Snapshot == nil || c.Snapshot.Have.Cluster != fleet.Collected {
		return s
	}
	s.statuses["control plane"] = c.Snapshot.Cluster.Status
	for _, n := range c.Snapshot.Nodegroups {
		if n.State == fleet.Collected {
			s.statuses["nodegroup "+n.Name] = n.Status
		}
	}
	for _, a := range c.Snapshot.Addons {
		if a.State == fleet.Collected {
			s.statuses["add-on "+a.Name] = a.Status
		}
	}
	return s
}
