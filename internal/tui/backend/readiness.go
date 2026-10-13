package backend

import (
	"context"
	"slices"
	"strings"

	"github.com/dantech2000/refresh/internal/apidoc"
	"github.com/dantech2000/refresh/internal/fleet"
	"github.com/dantech2000/refresh/internal/health"
	"github.com/dantech2000/refresh/internal/tui/state"
	"github.com/dantech2000/refresh/internal/view"
)

// Readiness starts the readiness checks of a cluster on a goroutine of
// the backend and returns at once. The result lands in a later state. One
// run per cluster at a time.
func (b *Live) Readiness(_ context.Context, key state.Key) error {
	b.mu.Lock()
	if !slices.ContainsFunc(b.clusters, func(c state.Cluster) bool { return c.Key == key }) {
		b.mu.Unlock()
		return ErrUnknownCluster
	}
	if r := b.readiness[key]; r != nil && r.Running() {
		b.mu.Unlock()
		return ErrReadinessRunning
	}
	b.readiness[key] = &state.Readiness{StartedAt: b.opts.Now()}
	b.mu.Unlock()
	b.bump()
	b.wg.Go(func() { b.runReadiness(key) })
	return nil
}

// runReadiness runs the checks under the read budget and publishes the
// report.
func (b *Live) runReadiness(key state.Key) {
	ctx, cancel := b.withTimeout()
	defer cancel()
	report, err := health.CheckReadiness(ctx, b.provider.Clients(key.Region), &fleet.Fetcher{Now: b.opts.Now}, key.Name, "")

	b.mu.Lock()
	// The entry is replaced, never written: a published state holds the
	// pointer of the run in progress.
	done := *b.readiness[key]
	done.EndedAt = b.opts.Now()
	if err != nil {
		done.Err = err.Error()
		b.say(key, view.Fail, "readiness: "+err.Error())
	} else {
		done.Report = &report
		b.say(key, readinessStatus(report.Readiness), "readiness: "+strings.ToLower(string(report.Readiness)))
	}
	b.readiness[key] = &done
	b.mu.Unlock()
	b.bump()
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
