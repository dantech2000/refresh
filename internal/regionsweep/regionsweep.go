// Package regionsweep runs one call per AWS region and sorts the regions into
// answered, skipped, and failed, the same way for every multi-region command
// (status -A, cluster list -A, nodegroup update --all-clusters).
package regionsweep

import (
	"context"
	"fmt"
	"sort"

	"github.com/dantech2000/refresh/internal/aws/awserr"
	"github.com/dantech2000/refresh/internal/common"
	"github.com/dantech2000/refresh/internal/diag"
)

// Options control a sweep.
type Options struct {
	// Concurrency caps the regions called at once (<= 0: the common default).
	Concurrency int
	// SkipInaccessible drops a region whose error says it is closed to these
	// credentials (awserr.IsRegionInaccessible) into Skipped instead of
	// Failed. Only the default region sweep sets it: a region the user named
	// must never disappear silently.
	SkipInaccessible bool
}

// Answer is one region that answered, with the call's value.
type Answer[T any] struct {
	Region string
	Value  T
}

// Result is the outcome of a sweep. Answered and Failed keep the region
// order of the sweep; Skipped is sorted.
type Result[T any] struct {
	Answered []Answer[T]
	// Failed has one failure (kind Region, eks:ListClusters) per region that
	// did not answer. A region the sweep never started because ctx ended is
	// ReasonNotAttempted.
	Failed []diag.Failure
	// Errors holds the error behind each failure, in the same order. A
	// failure is a one-line summary; callers that classify or report the
	// cause (runner.NoRegionAnswered) need the error itself.
	Errors []error
	// Skipped lists the regions closed to these credentials. They are not
	// failures.
	Skipped []string
}

// Run calls fn once per region, at most opts.Concurrency at a time, and
// sorts each region by its outcome. A region fn returns an error for is
// skipped or failed; to count a region as answered despite an error (rows
// that already carry their own failures), fn returns a nil error.
func Run[T any](ctx context.Context, regions []string, opts Options, fn func(ctx context.Context, region string) (T, error)) Result[T] {
	type outcome struct {
		ran   bool
		value T
		err   error
	}
	outcomes := common.ForEachParallel(ctx, regions, opts.Concurrency, func(rctx context.Context, region string) outcome {
		v, err := fn(rctx, region)
		return outcome{ran: true, value: v, err: err}
	})

	var res Result[T]
	for i, region := range regions {
		o := outcomes[i]
		switch {
		case !o.ran:
			// ctx ended before this region got a slot. It is a failure, so
			// an interrupted sweep never looks complete.
			err := fmt.Errorf("not queried: %w", context.Cause(ctx))
			f := diag.New(diag.KindRegion, region, diag.ReasonNotAttempted, err.Error())
			f.Region = region
			res.Failed = append(res.Failed, f)
			res.Errors = append(res.Errors, err)
		case o.err == nil:
			res.Answered = append(res.Answered, Answer[T]{Region: region, Value: o.value})
		case opts.SkipInaccessible && awserr.IsRegionInaccessible(o.err):
			res.Skipped = append(res.Skipped, region)
		default:
			res.Failed = append(res.Failed, diag.FromError(diag.KindRegion, region, diag.OpListClusters, o.err))
			res.Errors = append(res.Errors, o.err)
		}
	}
	sort.Strings(res.Skipped)
	return res
}
