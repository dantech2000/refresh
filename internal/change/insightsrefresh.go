package change

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"

	"github.com/dantech2000/refresh/internal/awsx"
)

const (
	// insightsRefreshLimit bounds the wait for an insights refresh. EKS
	// evaluates the insights about once a day on its own, so a real
	// upgrade asks for a refresh before it reads them.
	insightsRefreshLimit = 5 * time.Minute
	// insightsRefreshReuse is how long a refresh that completed counts as
	// current for the same cluster and control-plane version. The plan
	// after an answer and the readiness check before the first hop then
	// start no second refresh.
	insightsRefreshReuse = 10 * time.Minute

	codeInvalidRequest = "InvalidRequestException"
	codeNotFound       = "ResourceNotFoundException"
)

// errInsightsRefreshTimeout is the cause of a refresh that did not end
// within insightsRefreshLimit.
var errInsightsRefreshTimeout = errors.New("the insights refresh did not finish in time")

// refreshError is an error of the refresh that says, in the words of the
// refresher, what EKS answered. Its message keeps those words and the one
// line of the cause: a blocker shows it whole, where awsx.Summary would
// keep the API error alone.
type refreshError struct {
	msg string
	err error
}

func (e *refreshError) Error() string { return e.msg + ": " + awsx.Summary(e.err) }

func (e *refreshError) Unwrap() error { return e.err }

// refreshSummary returns the one line of a refresh error for a blocker:
// the words of the refresher and the cause, or the summary of an error
// that the refresher did not word.
func refreshSummary(err error) string {
	if re, ok := errors.AsType[*refreshError](err); ok {
		return re.Error()
	}
	return awsx.Summary(err)
}

// insightsRefresher starts the insights refreshes of one run and waits
// for them. A dry run has none: it starts no refresh.
//
// The refresh is the one request that a run may send before it has
// consent, and the one request that a blocked run sends: the decision to
// block needs fresh insights.
type insightsRefresher struct {
	d    Deps
	poll time.Duration
	// done has when a refresh completed, on the clock of the run, by
	// region, cluster, and control-plane version.
	done map[string]time.Time
	// ran counts the refreshes that were started or waited for. A plan
	// that sees it move knows that time passed since its read.
	ran int
	// failed says that the last refresh ended with an error. A plan that
	// sees it does not read the cluster again: a second refresh would
	// fail the same way, after a second wait.
	failed bool
	// progress reports what the wait does. It may be nil.
	progress func(text string)
	// hold stops the read budget of the caller while the refresh waits,
	// and returns the function that runs it again. It may be nil. The
	// refresh has its own limit.
	hold func() (release func())
}

func (ir *insightsRefresher) say(format string, args ...any) {
	if ir.progress != nil {
		ir.progress(fmt.Sprintf(format, args...))
	}
}

// refresh asks EKS to evaluate the insights of the cluster again and
// waits until the refresh has completed. live is the version that the
// control plane runs: a refresh that completed for it less than
// insightsRefreshReuse ago is reused, and nothing is sent.
//
// A start that EKS refuses is not always an error:
//
//   - A refresh is in progress already: the wait follows that one.
//   - EKS runs a scheduled refresh, which the describe call does not
//     show: the start is tried again on each poll until it is accepted.
//   - The cluster is new and has no insights yet: an error that says so.
//
// A refresh that ended counts only when it is newer than every refresh
// that the describe showed ended while the start was refused: after EKS
// accepts the start, the describe can still show that older one. Both
// start times are on the clock of EKS, so no skew of the clock of the run
// enters the comparison.
//
// The error is the error of ctx when ctx ended, and an error that wraps
// errInsightsRefreshTimeout when the limit of the refresh passed.
func (ir *insightsRefresher) refresh(ctx context.Context, t Target, live string) error {
	err := ir.wait(ctx, t, live)
	ir.failed = err != nil
	return err
}

// wait is refresh without the record of its outcome.
func (ir *insightsRefresher) wait(ctx context.Context, t Target, live string) error {
	key := t.Region + "|" + t.Cluster + "|" + live
	if at, ok := ir.done[key]; ok && ir.d.now().Sub(at) < insightsRefreshReuse {
		return nil
	}
	ir.ran++
	if ir.hold != nil {
		defer ir.hold()()
	}
	wctx, limit := ir.d.budget(ctx, insightsRefreshLimit, errInsightsRefreshTimeout)
	limit.start()
	defer limit.stop()
	clients, poll := ir.d.AWS.Clients(t.Region), ir.d.AWS.PollClients(t.Region)
	name := aws.String(t.Cluster)

	ir.say("refreshing the cluster insights of %s (waits up to %s)", t.Cluster, insightsRefreshLimit)
	began := ir.d.now()
	start := func() error {
		_, err := clients.EKS.StartInsightsRefresh(wctx, &eks.StartInsightsRefreshInput{ClusterName: name})
		return err
	}
	st := refreshStart{err: start()}
	o := WaitOptions{Interval: ir.poll, After: ir.d.after, budget: limit}
	for {
		if st.again {
			st.again = false
			st.err = start()
			switch {
			case awsx.ErrorCode(st.err) == codeInvalidRequest:
				st.rejected = st.err
			case st.err == nil, wctx.Err() == nil:
				st.rejected = nil
			}
		}
		out, err := poll.EKS.DescribeInsightsRefresh(wctx, &eks.DescribeInsightsRefreshInput{ClusterName: name})
		switch {
		case ctx.Err() != nil:
			return ctx.Err()
		case wctx.Err() != nil:
			return st.timedOut(t)
		case st.err != nil:
			if err := st.refused(ir, t, out, err); err != nil {
				return err
			}
		case err != nil:
			if awsx.IsPermanentAPIError(err) {
				return &refreshError{msg: "checking the insights refresh of cluster " + t.Cluster, err: err}
			}
			ir.say("checking the insights refresh of %s: %s; still waiting", t.Cluster, awsx.Summary(err))
		case st.old != nil && out.Status != ekstypes.InsightsRefreshStatusInProgress && !newerRefresh(out, st.old):
			// EKS accepted the start, and the describe still shows the
			// refresh that ended before. It says nothing about the
			// insights now.
			if !st.stale {
				ir.say("EKS still shows the earlier insights refresh of %s; waiting for the one this run started", t.Cluster)
			}
			st.stale = true
		case out.Status == ekstypes.InsightsRefreshStatusCompleted:
			if ir.done == nil {
				ir.done = map[string]time.Time{}
			}
			ir.done[key] = ir.d.now()
			return nil
		case out.Status == ekstypes.InsightsRefreshStatusFailed:
			msg := aws.ToString(out.Message)
			if msg == "" {
				msg = "no details reported"
			}
			return fmt.Errorf("the insights refresh of cluster %s failed: %s", t.Cluster, msg)
		default:
			ir.say("the insights refresh is in progress (%s so far)", ir.d.now().Sub(began).Round(time.Second))
		}
		if o.over(wctx) || !o.pause(wctx) {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return st.timedOut(t)
		}
	}
}

// refreshStart is the state of the start of one refresh in wait.
type refreshStart struct {
	// err is the refusal of the last start, nil once EKS accepted one or
	// the wait follows a refresh in progress.
	err error
	// rejected is the last refusal of a start that is read as a scheduled
	// refresh. The code has other meanings too, so a wait that ends with
	// the start still refused reports it.
	rejected error
	// again says to send the start once more before the next describe.
	again bool
	// noted says that the scheduled refresh was reported.
	noted bool
	// old is the refresh that ended before the run, from the describe
	// while the start was refused; nil when there was none. stale says
	// that the describe showed it again after EKS accepted the start.
	old   *eks.DescribeInsightsRefreshOutput
	stale bool
}

// refused handles the answer of the describe while the start is refused.
// An error ends the wait.
func (st *refreshStart) refused(ir *insightsRefresher, t Target, out *eks.DescribeInsightsRefreshOutput, err error) error {
	switch {
	case err == nil && out.Status == ekstypes.InsightsRefreshStatusInProgress:
		st.err, st.rejected = nil, nil
		ir.say("an insights refresh is already running for %s; waiting for it", t.Cluster)
	case awsx.ErrorCode(st.err) == codeInvalidRequest && awsx.ErrorCode(err) == codeNotFound:
		if !st.noted {
			st.noted = true
			ir.say("EKS is running a scheduled insights refresh for %s; waiting for it to end", t.Cluster)
		}
		st.rejected, st.again = st.err, true
	case awsx.ErrorCode(st.err) == codeInvalidRequest && err == nil:
		// The refresh that the describe shows has ended: a scheduled one
		// that ended since the start was refused, or an older one.
		// Neither is a refresh of this run, so the start is tried again,
		// and a refresh that ends later counts only when it is newer.
		st.rejected, st.again = st.err, true
		if st.old == nil || newerRefresh(out, st.old) {
			st.old = out
		}
	case awsx.ErrorCode(st.err) == codeNotFound:
		return &refreshError{msg: fmt.Sprintf("EKS has not made cluster insights available for %s yet (a new cluster can take 15 minutes); run again later", t.Cluster), err: st.err}
	default:
		return &refreshError{msg: "starting an insights refresh for cluster " + t.Cluster, err: st.err}
	}
	return nil
}

// timedOut returns the error of a wait that reached its limit.
func (st *refreshStart) timedOut(t Target) error {
	if st.rejected != nil {
		return &refreshError{msg: fmt.Sprintf("starting an insights refresh for cluster %s (still refused after %s)", t.Cluster, insightsRefreshLimit), err: st.rejected}
	}
	if st.stale {
		return fmt.Errorf("%w (%s): EKS still showed the refresh that started at %s, before this run asked for one",
			errInsightsRefreshTimeout, insightsRefreshLimit, orUnknown(timeText(aws.ToTime(st.old.StartedAt))))
	}
	return fmt.Errorf("%w (%s)", errInsightsRefreshTimeout, insightsRefreshLimit)
}

// newerRefresh reports whether the refresh that out describes started
// after the refresh old. A refresh with no start time is not known to be
// newer; any start time is newer than none.
func newerRefresh(out, old *eks.DescribeInsightsRefreshOutput) bool {
	return out.StartedAt != nil && (old.StartedAt == nil || out.StartedAt.After(*old.StartedAt))
}

// timeText is t in RFC 3339, in UTC, or "" for the zero time.
func timeText(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}
