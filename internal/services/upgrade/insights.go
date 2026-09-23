package upgrade

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"

	awsinternal "github.com/dantech2000/refresh/internal/aws"
	"github.com/dantech2000/refresh/internal/services/common"
)

// defaultInsightsRefreshTimeout bounds the wait for an on-demand insights
// refresh. EKS otherwise re-evaluates insights only about once a day, so the
// readiness gate refreshes them before it reads them.
const defaultInsightsRefreshTimeout = 5 * time.Minute

// insightsRefreshReuse is how long a completed refresh counts as current for
// the same cluster and control-plane version. It stops the execution-time
// re-gate of the first hop from repeating the refresh that plan generation
// ran moments earlier.
const insightsRefreshReuse = 10 * time.Minute

// skipInsightsHint is appended to every insights blocker.
const skipInsightsHint = "re-run shortly, or pass --skip-insights-check to upgrade without this check (not recommended)"

// errInsightsRefreshTimeout reports a refresh that did not finish in time.
var errInsightsRefreshTimeout = errors.New("insights refresh did not finish in time")

// refreshInsights asks EKS to re-evaluate the cluster's insights and waits
// until the refresh completes, fails, or InsightsRefreshTimeout passes. A
// refresh that completed for the same cluster and control-plane version
// within insightsRefreshReuse is reused.
func (s *Service) refreshInsights(ctx context.Context, clusterName, liveVersion string, progress ProgressFunc) error {
	progress = ensureProgress(progress)
	key := clusterName + "|" + liveVersion
	s.refreshMu.Lock()
	last, ok := s.refreshedAt[key]
	s.refreshMu.Unlock()
	if ok && time.Since(last) < insightsRefreshReuse {
		return nil
	}

	timeout := s.InsightsRefreshTimeout
	if timeout <= 0 {
		timeout = defaultInsightsRefreshTimeout
	}
	wctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	progress("refreshing cluster insights for %s (waits up to %s)", clusterName, timeout)
	started := time.Now()
	_, startErr := common.WithRetry(wctx, common.DefaultRetryConfig, func(rc context.Context) (*eks.StartInsightsRefreshOutput, error) {
		return s.eksClient.StartInsightsRefresh(rc, &eks.StartInsightsRefreshInput{ClusterName: aws.String(clusterName)})
	})
	// A start error can mean a refresh is already running (another tool or
	// an earlier run). Check once before giving up: if a refresh is in
	// flight, wait on it instead.

	interval := s.PollInterval
	if interval <= 0 {
		interval = defaultPollInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		out, err := common.WithRetry(wctx, common.DefaultRetryConfig, func(rc context.Context) (*eks.DescribeInsightsRefreshOutput, error) {
			return s.eksClient.DescribeInsightsRefresh(rc, &eks.DescribeInsightsRefreshInput{ClusterName: aws.String(clusterName)})
		})
		switch {
		case ctx.Err() != nil:
			return ctx.Err()
		case wctx.Err() != nil:
			return fmt.Errorf("%w after %s", errInsightsRefreshTimeout, timeout)
		case startErr != nil:
			if err != nil || out.Status != ekstypes.InsightsRefreshStatusInProgress {
				return awsinternal.FormatAWSError(startErr, fmt.Sprintf("starting an insights refresh for cluster %s", clusterName))
			}
			startErr = nil // another refresh is running; wait on it
			progress("an insights refresh is already running for %s; waiting on it", clusterName)
		case err != nil:
			if isPermanentAPIError(err) {
				return awsinternal.FormatAWSError(err, fmt.Sprintf("checking the insights refresh for cluster %s", clusterName))
			}
			progress("warning: checking the insights refresh for %s: %v", clusterName, err)
		case out.Status == ekstypes.InsightsRefreshStatusCompleted:
			s.refreshMu.Lock()
			if s.refreshedAt == nil {
				s.refreshedAt = map[string]time.Time{}
			}
			s.refreshedAt[key] = time.Now()
			s.refreshMu.Unlock()
			return nil
		case out.Status == ekstypes.InsightsRefreshStatusFailed:
			msg := aws.ToString(out.Message)
			if msg == "" {
				msg = "no details reported"
			}
			return fmt.Errorf("insights refresh failed: %s", msg)
		default:
			progress("insights refresh in progress (%s elapsed)", time.Since(started).Round(time.Second))
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-wctx.Done():
			return fmt.Errorf("%w after %s", errInsightsRefreshTimeout, timeout)
		case <-ticker.C:
		}
	}
}

// insightsVerdict evaluates the UPGRADE_READINESS insights for hopTo into a
// readiness status and reason. ERROR and UNKNOWN insights block; an empty
// list blocks too, because it means EKS has not evaluated hopTo yet and the
// deprecated-API and kubelet-skew checks have not run.
func insightsVerdict(hopTo string, insights []ekstypes.InsightSummary) (status StepStatus, reason string, warnings []string) {
	if len(insights) == 0 {
		return StatusBlocked, fmt.Sprintf("insights for %s not available yet: EKS hasn't evaluated this version; %s", hopTo, skipInsightsHint), nil
	}
	var errorsFound, unknownFound []string
	for _, in := range insights {
		name := aws.ToString(in.Name)
		if name == "" {
			name = aws.ToString(in.Id)
		}
		var value ekstypes.InsightStatusValue
		if in.InsightStatus != nil {
			value = in.InsightStatus.Status
		}
		switch value {
		case ekstypes.InsightStatusValuePassing:
		case ekstypes.InsightStatusValueWarning:
			warnings = append(warnings, name)
		case ekstypes.InsightStatusValueError:
			errorsFound = append(errorsFound, name)
		default: // UNKNOWN, missing, or a status this build doesn't know
			unknownFound = append(unknownFound, name)
		}
	}
	var blockers []string
	if len(errorsFound) > 0 {
		blockers = append(blockers, fmt.Sprintf("%d blocking insight(s): %s", len(errorsFound), strings.Join(errorsFound, ", ")))
	}
	if len(unknownFound) > 0 {
		blockers = append(blockers, fmt.Sprintf("%d insight(s) with UNKNOWN status (EKS could not evaluate them): %s; %s",
			len(unknownFound), strings.Join(unknownFound, ", "), skipInsightsHint))
	}
	if len(blockers) > 0 {
		return StatusBlocked, strings.Join(blockers, "; "), warnings
	}
	if len(warnings) > 0 {
		return StatusPending, fmt.Sprintf("%d insight warning(s): %s", len(warnings), strings.Join(warnings, ", ")), warnings
	}
	return StatusPending, fmt.Sprintf("%d insight(s) passing; skew OK", len(insights)), nil
}
