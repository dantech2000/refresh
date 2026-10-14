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
// The error is the error of ctx when ctx ended, and an error that wraps
// errInsightsRefreshTimeout when the limit of the refresh passed.
func (ir *insightsRefresher) refresh(ctx context.Context, t Target, live string) error {
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
	startErr := start()
	// rejected is the last refusal of a start that is read as a scheduled
	// refresh. The code has other meanings too, so a wait that ends with
	// the start still refused reports it.
	var rejected error
	startAgain, noted := false, false
	timedOut := func() error {
		if rejected != nil {
			return fmt.Errorf("starting an insights refresh for cluster %s (still refused after %s): %w", t.Cluster, insightsRefreshLimit, rejected)
		}
		return fmt.Errorf("%w (%s)", errInsightsRefreshTimeout, insightsRefreshLimit)
	}
	o := WaitOptions{Interval: ir.poll, After: ir.d.after, budget: limit}
	for {
		if startAgain {
			startAgain = false
			startErr = start()
			switch {
			case awsx.ErrorCode(startErr) == codeInvalidRequest:
				rejected = startErr
			case startErr == nil, wctx.Err() == nil:
				rejected = nil
			}
		}
		out, err := poll.EKS.DescribeInsightsRefresh(wctx, &eks.DescribeInsightsRefreshInput{ClusterName: name})
		switch {
		case ctx.Err() != nil:
			return ctx.Err()
		case wctx.Err() != nil:
			return timedOut()
		case startErr != nil:
			switch {
			case err == nil && out.Status == ekstypes.InsightsRefreshStatusInProgress:
				startErr, rejected = nil, nil
				ir.say("an insights refresh is already running for %s; waiting for it", t.Cluster)
			case awsx.ErrorCode(startErr) == codeInvalidRequest && awsx.ErrorCode(err) == codeNotFound:
				if !noted {
					noted = true
					ir.say("EKS is running a scheduled insights refresh for %s; waiting for it to end", t.Cluster)
				}
				rejected, startAgain = startErr, true
			case awsx.ErrorCode(startErr) == codeNotFound:
				return fmt.Errorf("EKS has not made cluster insights available for %s yet (a new cluster can take 15 minutes); run again later: %w", t.Cluster, startErr)
			default:
				return fmt.Errorf("starting an insights refresh for cluster %s: %w", t.Cluster, startErr)
			}
		case err != nil:
			if awsx.IsPermanentAPIError(err) {
				return fmt.Errorf("checking the insights refresh of cluster %s: %w", t.Cluster, err)
			}
			ir.say("checking the insights refresh of %s: %s; still waiting", t.Cluster, awsx.Summary(err))
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
			return timedOut()
		}
	}
}
