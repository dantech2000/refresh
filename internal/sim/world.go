// Package sim is a simulated EKS fleet for the refresh TUI. It runs on a
// virtual clock and implements state.Backend, so every screen (fleet status,
// readiness, nodegroup rolls, add-on updates, cluster upgrades) can be driven
// end to end with no AWS account and no cluster.
//
// The simulation is deterministic: the same Options and the same calls give
// the same states. Tests step it with Advance; refresh ui --simulate runs it
// in real time with Run.
//
// Node lifecycle events come from the real noderoll.Tracker, fed with
// noderoll.Snapshots built from the simulated nodes, so the roll feed uses the
// same event logic as a live roll.
package sim

import (
	"context"
	"fmt"
	"math/rand/v2"
	"slices"
	"sync"
	"time"

	"github.com/dantech2000/refresh/internal/tui/state"
)

// step is the simulation's time resolution.
const step = time.Second

// Event list bounds. The TUI only draws the tail of each list.
const (
	feedCap = 500
	logCap  = 400
)

// Options configures a World.
type Options struct {
	// Seed makes the simulated timings reproducible.
	Seed uint64
	// Start is the simulated wall clock at creation. Zero uses a fixed date.
	Start time.Time
	// Warmup advances the world this far after the fleet is created, with a
	// cluster upgrade already running, so the first screen has live data.
	// Zero skips the warmup.
	Warmup time.Duration
}

// World is the simulated fleet. It is safe for concurrent use.
type World struct {
	mu    sync.Mutex
	epoch time.Time
	now   time.Time
	carry time.Duration
	rng   *rand.Rand

	clusters  []*cluster
	feed      []state.Event
	log       []state.Event
	readiness map[string]*readiness
	// checkRuns holds every readiness run in start order, so ticks step
	// them in a fixed order (map order would break determinism).
	checkRuns []*readiness
	rolls     []*roll
	addonRuns []*addonRun
	upgrades  []*upgrade
	syncedAt  time.Time
	nextSync  time.Time
	timed     []timedEvent
	seq       int
	// evSeq numbers every event the world records (state.Event.Seq).
	evSeq uint64
}

// timedEvent is a scripted fleet change at a set time.
type timedEvent struct {
	at time.Time
	fn func()
}

// DefaultStart is the simulated clock's start when Options.Start is zero.
var DefaultStart = time.Date(2026, 9, 24, 13, 40, 0, 0, time.UTC)

// New builds the simulated fleet.
func New(opts Options) *World {
	start := opts.Start
	if start.IsZero() {
		start = DefaultStart
	}
	w := &World{
		epoch:     start,
		now:       start,
		rng:       rand.New(rand.NewPCG(opts.Seed, opts.Seed^0x9e3779b97f4a7c15)), //nolint:gosec // reproducible simulated timings, not a secret
		readiness: map[string]*readiness{},
		syncedAt:  start,
		nextSync:  start.Add(15 * time.Second),
	}
	w.clusters = fleet(start)
	w.schedule()
	if opts.Warmup > 0 {
		_ = w.startUpgrade("prod-eu")
		w.Advance(opts.Warmup)
	}
	return w
}

// schedule queues the scripted fleet changes that make the world feel alive
// while nobody starts anything.
func (w *World) schedule() {
	w.timed = append(w.timed,
		timedEvent{at: w.now.Add(4 * time.Minute), fn: func() {
			c := w.cluster("stage-api")
			if c == nil {
				return
			}
			for i := range c.Nodegroups {
				ng := &c.Nodegroups[i]
				if ng.Version == c.Version {
					ng.LatestAMI = amiRelease(c.Version, "20260925")
				}
			}
			w.emit(state.Event{Cluster: c.Name, Source: state.SourceAWS, Level: state.LevelWarn,
				Subject: "ssm", Text: "new EKS AMI release for " + c.Version, Detail: amiRelease(c.Version, "20260925")})
		}},
	)
}

// Now returns the simulated time.
func (w *World) Now() time.Time {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.now
}

// Advance moves the simulated clock forward by d, running every change in
// flight in one-second steps. A remainder under a second carries over.
func (w *World) Advance(d time.Duration) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.carry += d
	for w.carry >= step {
		w.carry -= step
		w.now = w.now.Add(step)
		w.tick()
	}
}

// Run advances the world in real time, speed simulated seconds per real
// second, until ctx ends.
func (w *World) Run(ctx context.Context, speed float64) {
	const frame = 100 * time.Millisecond
	t := time.NewTicker(frame)
	defer t.Stop()
	w.run(ctx, t.C, frame, speed)
}

// run advances the world by frame*speed on every tick until ctx ends.
func (w *World) run(ctx context.Context, ticks <-chan time.Time, frame time.Duration, speed float64) {
	if speed <= 0 {
		speed = 1
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticks:
			w.Advance(time.Duration(float64(frame) * speed))
		}
	}
}

func (w *World) tick() {
	for len(w.timed) > 0 && !w.now.Before(w.timed[0].at) {
		ev := w.timed[0]
		w.timed = w.timed[1:]
		ev.fn()
	}
	if !w.now.Before(w.nextSync) {
		w.syncedAt = w.now
		w.nextSync = w.now.Add(15 * time.Second)
		w.api("", "ListClusters", "4 regions · 6 clusters")
	}
	for _, r := range w.checkRuns {
		r.step()
	}
	for _, r := range w.rolls {
		r.step()
	}
	for _, a := range w.addonRuns {
		a.step()
	}
	for _, u := range w.upgrades {
		u.step()
	}
}

// stamp sets an event's time to now and gives it the next sequence number.
func (w *World) stamp(e state.Event) state.Event {
	w.evSeq++
	e.Seq = w.evSeq
	e.At = w.now
	return e
}

// emit appends e to the fleet feed at the current time.
func (w *World) emit(e state.Event) {
	w.feed = appendCapped(w.feed, w.stamp(e), feedCap)
}

// api records an AWS API call in the fleet log and returns the event.
func (w *World) api(cluster, op, text string) state.Event {
	e := w.stamp(state.Event{Cluster: cluster, Source: state.SourceAWS, Level: state.LevelInfo, Subject: op, Text: text})
	w.log = appendCapped(w.log, e, logCap)
	return e
}

func appendCapped(list []state.Event, e state.Event, max int) []state.Event {
	list = append(list, e)
	if over := len(list) - max; over > 0 {
		n := copy(list, list[over:])
		list = list[:n]
	}
	return list
}

// between returns a random duration in [lo, hi], rounded to the step.
func (w *World) between(lo, hi time.Duration) time.Duration {
	if hi <= lo {
		return lo
	}
	d := lo + time.Duration(w.rng.Int64N(int64(hi-lo)+1))
	return d.Round(step)
}

// id returns a short unique hex id.
func (w *World) id() string {
	w.seq++
	return fmt.Sprintf("%04x%04x", w.rng.IntN(0x10000), w.seq)
}

func (w *World) cluster(name string) *cluster {
	for _, c := range w.clusters {
		if c.Name == name {
			return c
		}
	}
	return nil
}

// State implements state.Backend.
func (w *World) State(ctx context.Context) (state.State, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	// A call cancelled while it waited for the lock returns no state.
	if err := ctx.Err(); err != nil {
		return state.State{}, err
	}
	return w.snapshot(), nil
}

// Snapshot returns the world's state; State without the Backend plumbing.
func (w *World) Snapshot() state.State {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.snapshot()
}

// snapshot copies the world's state. The caller holds w.mu.
func (w *World) snapshot() state.State {
	st := state.State{
		Now:             w.now,
		Seq:             w.evSeq,
		Backend:         "simulated",
		Badge:           "SIMULATED",
		Context:         "prod",
		Profile:         "prod-admin",
		RegionsAnswered: 4,
		RegionsTotal:    4,
		SyncedAt:        w.syncedAt,
		Feed:            slices.Clone(w.feed),
		Log:             slices.Clone(w.log),
		Readiness:       make(map[string]state.Readiness, len(w.readiness)),
	}
	for _, c := range w.clusters {
		cc := c.Cluster
		cc.Nodegroups = slices.Clone(c.Nodegroups)
		cc.Addons = slices.Clone(c.Addons)
		st.Clusters = append(st.Clusters, cc)
	}
	for name, r := range w.readiness {
		st.Readiness[name] = r.snapshot()
	}
	for _, r := range w.rolls {
		st.Rolls = append(st.Rolls, r.snapshot())
	}
	for _, u := range w.upgrades {
		st.Upgrades = append(st.Upgrades, u.snapshot())
	}
	return st
}

// RunReadiness implements state.Backend.
func (w *World) RunReadiness(ctx context.Context, name string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	// A call cancelled while it waited for the lock changes nothing.
	if err := ctx.Err(); err != nil {
		return err
	}
	c := w.cluster(name)
	if c == nil {
		return fmt.Errorf("cluster %q not found", name)
	}
	if r := w.readiness[name]; r != nil && r.st.Running {
		return nil
	}
	w.readiness[name] = w.newReadiness(c, nil)
	return nil
}

// Plan implements state.Backend.
func (w *World) Plan(ctx context.Context, a state.Action) (state.Plan, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	// A call cancelled while it waited for the lock changes nothing.
	if err := ctx.Err(); err != nil {
		return state.Plan{}, err
	}
	return w.plan(a)
}

// plan dry-runs a. The caller holds w.mu.
func (w *World) plan(a state.Action) (state.Plan, error) {
	c := w.cluster(a.Cluster)
	if c == nil {
		return state.Plan{}, fmt.Errorf("cluster %q not found", a.Cluster)
	}
	switch a.Kind {
	case state.ActionRoll:
		return w.planRoll(c, a.Nodegroup)
	case state.ActionAddons:
		return w.planAddons(c)
	case state.ActionUpgrade:
		return w.planUpgrade(c)
	default:
		return state.Plan{}, fmt.Errorf("unknown action %d", a.Kind)
	}
}

// Start implements state.Backend. It plans again and starts under one lock,
// so a gate that closed since the dry run (or a second Start racing this
// one) still blocks.
func (w *World) Start(ctx context.Context, a state.Action) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	// A call cancelled while it waited for the lock changes nothing.
	if err := ctx.Err(); err != nil {
		return err
	}
	p, err := w.plan(a)
	if err != nil {
		return err
	}
	if p.Blocked != "" {
		return fmt.Errorf("blocked: %s", p.Blocked)
	}
	c := w.cluster(a.Cluster)
	switch a.Kind {
	case state.ActionRoll:
		_, err := w.startRoll(c, a.Nodegroup, nil, "")
		return err
	case state.ActionAddons:
		w.startAddons(c, nil, "")
		return nil
	default:
		return w.startUpgrade(a.Cluster)
	}
}

// StopAfterCurrent implements state.Backend.
func (w *World) StopAfterCurrent(ctx context.Context, name string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	// A call cancelled while it waited for the lock changes nothing.
	if err := ctx.Err(); err != nil {
		return err
	}
	u := w.runningUpgrade(name)
	if u == nil {
		return fmt.Errorf("no upgrade is running on %s", name)
	}
	u.st.StopAfter = !u.st.StopAfter
	return nil
}

// TogglePause implements state.Backend.
func (w *World) TogglePause(ctx context.Context, name string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	// A call cancelled while it waited for the lock changes nothing.
	if err := ctx.Err(); err != nil {
		return err
	}
	u := w.runningUpgrade(name)
	if u == nil {
		return fmt.Errorf("no upgrade is running on %s", name)
	}
	u.st.Paused = !u.st.Paused
	return nil
}

// Answer implements state.Backend. Simulated upgrades never ask.
func (w *World) Answer(ctx context.Context, name string, _ bool) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	return fmt.Errorf("the upgrade of %s is not waiting on a question", name)
}

func (w *World) runningUpgrade(name string) *upgrade {
	for _, u := range w.upgrades {
		if u.st.Cluster == name && u.st.Running() {
			return u
		}
	}
	return nil
}

var _ state.Backend = (*World)(nil)
