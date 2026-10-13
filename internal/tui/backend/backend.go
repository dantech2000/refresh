// Package backend reads the fleet for the TUI and publishes it as versioned
// states. It sweeps the regions on an interval, runs readiness checks on
// demand, and never writes to a terminal. The model waits for the next
// version in Next; nothing polls, and nothing runs when nothing changed.
package backend

import (
	"context"
	"errors"
	"slices"
	"sync"
	"time"

	"github.com/dantech2000/refresh/internal/awsx"
	"github.com/dantech2000/refresh/internal/cmdutil"
	"github.com/dantech2000/refresh/internal/fleet"
	"github.com/dantech2000/refresh/internal/tui/state"
	"github.com/dantech2000/refresh/internal/view"
)

// The errors that a call of the backend returns.
var (
	ErrReadOnly         = errors.New("the UI is read-only; start it with --allow-changes")
	ErrNotBuilt         = errors.New("changes from the UI are not built yet; run the CLI command")
	ErrReadinessRunning = errors.New("the readiness checks of this cluster are running")
	ErrUnknownCluster   = errors.New("the cluster is not in the fleet")
)

// Options say what the backend reads and how.
type Options struct {
	Regions fleet.Regions
	// Interval is the time between two sweeps.
	Interval time.Duration
	// AllowChanges is --allow-changes. It is read once, here, and enforced
	// in Start.
	AllowChanges bool
	// ReadTimeout bounds a sweep and a readiness run: --timeout. Zero
	// means no limit.
	ReadTimeout time.Duration
	// Profile and Context are shown in the top bar.
	Profile, Context string
	// SweepError, when set, explains a sweep in which no region answered,
	// as the read commands do. Nil uses the first failure.
	SweepError func(ctx context.Context, res *fleet.SweepResult) error
	// Now and After are the clock. Nil means time.Now and time.After.
	Now   func() time.Time
	After func(time.Duration) <-chan time.Time
}

// Live is the backend over a real fleet.
type Live struct {
	ctx      context.Context //nolint:containedctx // the context of the session, which every goroutine derives from
	provider *awsx.Provider
	opts     Options
	refresh  chan struct{}
	wg       sync.WaitGroup

	mu           sync.Mutex
	version      uint64
	changed      chan struct{} // closed and replaced by bump
	sweep        state.Sweep
	rowsByRegion map[string][]state.Cluster
	clusters     []state.Cluster
	previous     map[state.Key]summary // the rows of the sweep before, for the feed
	readiness    map[state.Key]*state.Readiness
	feed         []state.Line
	// skippedBefore are the skipped regions of the sweep before: a feed
	// line names them when the list changes.
	skippedBefore []string
}

// feedLines is how many lines the feed keeps.
const feedLines = 500

// New returns a backend over provider. ctx is the context of the session:
// every goroutine of the backend ends when it does.
func New(ctx context.Context, provider *awsx.Provider, o Options) *Live {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.After == nil {
		o.After = time.After
	}
	return &Live{
		ctx:          ctx,
		provider:     provider,
		opts:         o,
		refresh:      make(chan struct{}, 1),
		changed:      make(chan struct{}),
		rowsByRegion: map[string][]state.Cluster{},
		readiness:    map[state.Key]*state.Readiness{},
	}
}

// Run sweeps the fleet until the context of the session ends, and then
// waits for every goroutine of the backend. A sweep runs at once, then
// every Interval, and when Refresh asks for one.
func (b *Live) Run() {
	defer b.wg.Wait()
	for {
		b.doSweep()
		select {
		case <-b.ctx.Done():
			return
		case <-b.opts.After(b.opts.Interval):
		case <-b.refresh:
		}
	}
}

// Refresh asks for a sweep now. It never blocks: a sweep that is already
// asked for is one sweep.
func (b *Live) Refresh() {
	select {
	case b.refresh <- struct{}{}:
	default:
	}
}

// Start would run a proposal. Without --allow-changes it is refused here,
// and nowhere else: this is the one function that will call change.Start.
func (b *Live) Start(context.Context, state.ProposalID) error {
	if !b.opts.AllowChanges {
		return ErrReadOnly
	}
	return ErrNotBuilt
}

// Next returns the first state whose version is above after. It blocks
// until there is one, or ctx ends.
func (b *Live) Next(ctx context.Context, after uint64) (state.State, error) {
	for {
		b.mu.Lock()
		if b.version > after {
			st := b.build()
			b.mu.Unlock()
			return st, nil
		}
		ch := b.changed
		b.mu.Unlock()
		select {
		case <-ch:
		case <-ctx.Done():
			return state.State{}, ctx.Err()
		}
	}
}

// bump records a change and wakes Next. It never blocks.
func (b *Live) bump() {
	b.mu.Lock()
	b.version++
	close(b.changed)
	b.changed = make(chan struct{})
	b.mu.Unlock()
}

// build returns the state at the current version. The caller holds b.mu.
// The rows are copied so that a readiness run can be attached to its
// cluster without a write to a published slice; the snapshots behind them
// are shared, and never written to.
func (b *Live) build() state.State {
	clusters := slices.Clone(b.clusters)
	for i := range clusters {
		clusters[i].Readiness = b.readiness[clusters[i].Key]
	}
	return state.State{
		Version:  b.version,
		Now:      b.opts.Now(),
		ReadOnly: !b.opts.AllowChanges,
		Profile:  b.opts.Profile,
		Context:  b.opts.Context,
		Sweep:    b.sweep,
		Clusters: clusters,
		Feed:     b.feed,
	}
}

// say appends a line to the feed. The caller holds b.mu. A published
// state keeps the slice it got: an append never writes inside its length,
// and a cut copies.
func (b *Live) say(cluster state.Key, status view.Status, text string) {
	b.feed = append(b.feed, state.Line{At: b.opts.Now(), Cluster: cluster, Status: status, Text: text})
	if len(b.feed) > feedLines {
		b.feed = slices.Clone(b.feed[len(b.feed)-feedLines:])
	}
}

// withTimeout bounds a read of the backend by ReadTimeout.
func (b *Live) withTimeout() (context.Context, context.CancelFunc) {
	return cmdutil.WithTimeout(b.ctx, b.opts.ReadTimeout)
}
