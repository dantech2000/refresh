package fleet

import (
	"context"
	"slices"
	"sync"

	"github.com/dantech2000/refresh/internal/apidoc"
	"github.com/dantech2000/refresh/internal/awsx"
	"github.com/dantech2000/refresh/internal/fanout"
)

// Regions is the region list of a sweep.
type Regions struct {
	Names []string
	// Default is true when nobody named the regions: they are the built-in
	// list for the partition. Only a default sweep skips a region that is
	// closed to the credentials.
	Default bool
}

// SweepOptions say what a sweep reads.
type SweepOptions struct {
	Need Need
	// Match, when set, selects clusters by region and name before any
	// describe call.
	Match func(region, name string) bool
	// OnRegion, when set, gets the snapshots of each region that answers,
	// as soon as the region is done. Calls do not overlap. It must not
	// block.
	OnRegion func(region string, snaps []*Snapshot)
}

// SweepResult is the outcome of a sweep.
type SweepResult struct {
	Regions Regions
	// Answered are the regions that returned a cluster list, in the order
	// of the region list. A region with no clusters is answered, and so
	// is one with incomplete rows.
	Answered []string
	// Skipped are the regions of a default sweep that are closed to the
	// credentials, sorted. They are not failures.
	Skipped []string
	// Snapshots has one snapshot for each selected cluster of each
	// answered region, in region order and then by name.
	Snapshots []*Snapshot
	// Failures has one Region failure for each region that did not answer,
	// in the order of the region list.
	Failures []apidoc.Failure
	// Errs holds the error behind each of Failures, in the same order. A
	// caller that must tell bad credentials from a closed region needs the
	// error itself.
	Errs []error
}

// regionOutcome is what one region of a sweep returned.
type regionOutcome struct {
	ran   bool // false when the context ended before the region started
	snaps []*Snapshot
	err   error // the error of the cluster list
}

// Sweep reads the clusters of every region in r. All the regions start at
// once. In each region the request limit of p bounds the calls, and the
// sweep works on at most that many clusters at a time, or half as many
// when a cluster makes several calls at once, in name order, so that rows
// finish in order: when ctx ends, the rows before the ones in
// work are complete and the rows after them are marked not attempted.
//
// A region of a default sweep whose cluster list fails because the region
// is closed to the credentials is skipped. A named region is never skipped.
func (f *Fetcher) Sweep(ctx context.Context, p *awsx.Provider, r Regions, opts SweepOptions) *SweepResult {
	var report sync.Mutex
	outcomes := fanout.ForEach(ctx, r.Names, max(len(r.Names), 1), func(ctx context.Context, region string) regionOutcome {
		// Only a default sweep gives up on a region after one failed
		// dial. A region that the user named gets the full retries.
		clients := p.Clients(region)
		if r.Default {
			clients = p.SweepClients(region).Clients
		}
		snaps, err := f.sweepRegion(ctx, clients, p.MaxInFlight(), opts)
		if err == nil && opts.OnRegion != nil {
			report.Lock()
			opts.OnRegion(region, snaps)
			report.Unlock()
		}
		return regionOutcome{ran: true, snaps: snaps, err: err}
	})

	res := &SweepResult{Regions: r}
	for i, region := range r.Names {
		o := outcomes[i]
		switch {
		case !o.ran:
			res.Failures = append(res.Failures, notAttempted(ctx, apidoc.FailureRegion, region))
			res.Errs = append(res.Errs, ctx.Err())
		case o.err == nil:
			res.Answered = append(res.Answered, region)
			res.Snapshots = append(res.Snapshots, o.snaps...)
		case r.Default && awsx.IsRegionInaccessible(o.err):
			res.Skipped = append(res.Skipped, region)
		default:
			res.Failures = append(res.Failures, FailureFromError(apidoc.FailureRegion, region, o.err))
			res.Errs = append(res.Errs, o.err)
		}
	}
	slices.Sort(res.Skipped)
	return res
}

// sweepRegion reads the selected clusters of one region. The error is the
// error of the cluster list.
func (f *Fetcher) sweepRegion(ctx context.Context, c *awsx.Clients, limit int, opts SweepOptions) ([]*Snapshot, error) {
	names, err := f.Clusters(ctx, c)
	if err != nil {
		return nil, err
	}
	if opts.Match != nil {
		names = slices.DeleteFunc(names, func(name string) bool { return !opts.Match(c.Region, name) })
	}
	slices.Sort(names)
	if limit <= 0 {
		limit = max(len(names), 1)
	} else if opts.Need.wide() {
		// A cluster that reads more than itself has two or more calls to
		// make at any time, so half as many clusters in work still fill
		// the request slots. Each cluster in work costs goroutines and
		// their stacks, which were most of the memory of a large sweep.
		limit = max(limit/2, 1)
	}
	snaps := fanout.ForEach(ctx, names, limit, func(ctx context.Context, name string) *Snapshot {
		// The error is in the snapshot too, as the failure of the row.
		snap, _ := f.Fetch(ctx, c, name, opts.Need)
		return snap
	})
	for i, snap := range snaps {
		if snap == nil {
			// The context ended before this cluster started.
			failure := notAttempted(ctx, apidoc.FailureCluster, names[i])
			failure.Region = c.Region
			snaps[i] = &Snapshot{
				Cluster:  Cluster{Name: names[i], Region: c.Region},
				Have:     Have{Cluster: NotAttempted},
				Failures: []apidoc.Failure{failure},
			}
		}
	}
	return snaps, nil
}
