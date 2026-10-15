// Package backend reads the fleet for the TUI and publishes it as versioned
// states. It sweeps the regions on an interval, runs readiness checks on
// demand, and never writes to a terminal. The model waits for the next
// version in Next; nothing polls, and nothing runs when nothing changed.
package backend

import (
	"cmp"
	"context"
	"errors"
	"slices"
	"sync"
	"time"

	"github.com/dantech2000/refresh/internal/awsx"
	"github.com/dantech2000/refresh/internal/change"
	"github.com/dantech2000/refresh/internal/cmdutil"
	"github.com/dantech2000/refresh/internal/fleet"
	"github.com/dantech2000/refresh/internal/tui/state"
	"github.com/dantech2000/refresh/internal/view"
)

// The errors that a call of the backend returns.
var (
	ErrReadOnly         = errors.New("the UI is read-only; start it with --allow-changes")
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
	// ReadTimeout bounds a sweep, a readiness run, and the reads of a
	// change: --timeout. Zero means no limit.
	ReadTimeout time.Duration
	// WaitTimeout bounds a change the UI starts or watches. With
	// WaitTimeoutSet false each kind of change gets the default of its
	// command; 0 with it set means no limit.
	WaitTimeout    time.Duration
	WaitTimeoutSet bool
	// Deps are what a change needs: the clients, the Kubernetes access,
	// and the clock.
	Deps change.Deps
	// Profile and Context are shown in the top bar. Profile, Kubeconfig,
	// and KubeContext go into the command a dry run names.
	Profile, Context string
	// ProfileInEffect is the profile that the run uses, from --profile,
	// the context, or AWS_PROFILE. The top bar shows it when it is set.
	// It does not go into a command: a profile from the environment is
	// the environment's to set.
	ProfileInEffect         string
	Kubeconfig, KubeContext string
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
	deps     change.Deps
	opts     Options
	refresh  chan struct{}
	// refetch carries the cluster of a change that ended, for one read of
	// it between sweeps.
	refetch chan state.Key
	wg      sync.WaitGroup

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

	// entries are the changes the UI started or watches, oldest first.
	// byCluster has the running ones of each cluster: the claim. ended is
	// when the last own change of a cluster ended, for adoption.
	entries   []*entry
	byCluster map[state.Key][]state.ChangeID
	ended     map[state.Key]time.Time
	// proposal is the newest dry run, which Start consumes.
	proposal     *proposal
	nextProposal state.ProposalID
	nextChange   state.ChangeID
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
	deps := o.Deps
	if deps.AWS == nil {
		deps.AWS = provider
	}
	if deps.Fetch == nil {
		deps.Fetch = &fleet.Fetcher{Now: o.Now}
	}
	if deps.Now == nil {
		deps.Now = o.Now
	}
	if deps.After == nil {
		deps.After = o.After
	}
	return &Live{
		ctx:          ctx,
		provider:     provider,
		deps:         deps,
		opts:         o,
		refresh:      make(chan struct{}, 1),
		refetch:      make(chan state.Key, 16),
		changed:      make(chan struct{}),
		rowsByRegion: map[string][]state.Cluster{},
		readiness:    map[state.Key]*state.Readiness{},
		byCluster:    map[state.Key][]state.ChangeID{},
		ended:        map[state.Key]time.Time{},
	}
}

// Run sweeps the fleet until the context of the session ends, and then
// waits for every goroutine of the backend. A sweep runs at once, then
// every Interval, and when Refresh asks for one; between sweeps, a
// cluster whose change ended is read again on its own.
func (b *Live) Run() {
	defer b.wg.Wait()
	for {
		b.doSweep()
		for {
			select {
			case <-b.ctx.Done():
				return
			case <-b.opts.After(b.opts.Interval):
			case <-b.refresh:
			case key := <-b.refetch:
				b.refetchCluster(key)
				continue
			}
			break
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
	b.bumpLocked()
	b.mu.Unlock()
}

// build returns the state at the current version. The caller holds b.mu.
// The rows are copied so that a readiness run can be attached to its
// cluster without a write to a published slice; the snapshots behind them
// are shared, and never written to.
func (b *Live) build() state.State {
	clusters := slices.Clone(b.clusters)
	for i := range clusters {
		c := &clusters[i]
		c.Readiness = b.readiness[c.Key]
		if c.Readiness != nil && c.Readiness.Report != nil {
			c.Rollback = c.Readiness.Report.Rollback
		}
		c.Change = b.running(c.Key)
	}
	return state.State{
		Version:  b.version,
		Now:      b.opts.Now(),
		ReadOnly: !b.opts.AllowChanges,
		Profile:  cmp.Or(b.opts.ProfileInEffect, b.opts.Profile),
		Context:  b.opts.Context,
		Sweep:    b.sweep,
		Clusters: clusters,
		Changes:  b.changes(),
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
	return b.withTimeoutOf(b.ctx)
}

// withTimeoutOf bounds a read under ctx by ReadTimeout.
func (b *Live) withTimeoutOf(ctx context.Context) (context.Context, context.CancelFunc) {
	return cmdutil.WithTimeout(ctx, b.opts.ReadTimeout)
}

// bumpLocked is bump for a caller that holds b.mu.
func (b *Live) bumpLocked() {
	b.version++
	close(b.changed)
	b.changed = make(chan struct{})
}
