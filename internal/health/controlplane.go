package health

import (
	"context"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	cwtypes "github.com/aws/aws-sdk-go-v2/service/cloudwatch/types"
)

// EKS standard control-plane etcd limit. At 8 GiB etcd raises a no-space alarm
// and the API server goes read-only (writes rejected) — so a cluster near this
// line is unsafe to upgrade until the database is compacted/trimmed.
// https://docs.aws.amazon.com/eks/latest/best-practices/known_limits_and_service_quotas.html
const etcdQuotaBytes = 8 * 1024 * 1024 * 1024

// Control-plane gate thresholds (percent). etcd usage is the blocking signal;
// the API-server error rate is advisory (warn-only) so noisy request metrics
// never wrongly block an upgrade.
const (
	etcdFailPercent = 95.0 // near read-only — block
	etcdWarnPercent = 80.0 // getting tight — warn

	apiserver5xxWarnPercent = 2.0    // server-error ratio over the window
	apiserverMinReqsForRate = 1000.0 // ignore the ratio below this volume (too noisy)

	// controlPlaneWindow is how far back to aggregate. The AWS/EKS metrics are
	// emitted per-minute; a 15-minute window smooths a single blip.
	controlPlaneWindow = 15 * time.Minute
)

// metricDataAPI is the slice of CloudWatch the control-plane gate needs. The
// concrete *cloudwatch.Client satisfies it; tests pass a fake.
type metricDataAPI interface {
	GetMetricData(ctx context.Context, in *cloudwatch.GetMetricDataInput, optFns ...func(*cloudwatch.Options)) (*cloudwatch.GetMetricDataOutput, error)
}

// controlPlaneMetrics holds the control-plane signals aggregated over the
// window. The *Known flags distinguish "measured zero" from "not emitted".
type controlPlaneMetrics struct {
	etcdInUseBytes float64
	etcdKnown      bool
	reqTotal       float64
	req5xx         float64
	req429         float64
	pendingPods    float64
	pendingKnown   bool
	// hasData is true when CloudWatch returned at least one datapoint, i.e. the
	// AWS/EKS control-plane metrics are being emitted (cluster is ≥1.28 on a
	// supported platform version). When false, the gate is Skipped.
	hasData bool
}

// CheckControlPlaneMetrics gates upgrade readiness on the EKS control plane:
// etcd database usage vs the 8 GiB read-only limit, plus the API-server error
// rate. It reads the free AWS/EKS CloudWatch metrics (no Container Insights /
// agent). Clusters below 1.28 don't emit these — the check is then Skipped, not
// failed. This is a readiness GATE, not a utilization-browse surface (REF-78).
func (hc *HealthChecker) CheckControlPlaneMetrics(ctx context.Context, clusterName string) HealthResult {
	if hc.cwClient == nil {
		return HealthResult{
			Name:    "Control Plane",
			Status:  StatusPass,
			Skipped: true,
			Message: "control-plane metrics unavailable (no CloudWatch client)",
		}
	}
	return checkControlPlaneMetrics(ctx, hc.cwClient, clusterName)
}

// checkControlPlaneMetrics is CheckControlPlaneMetrics against any
// GetMetricData-capable client, so it is testable with a fake.
func checkControlPlaneMetrics(ctx context.Context, api metricDataAPI, clusterName string) HealthResult {
	m, err := fetchControlPlaneMetrics(ctx, api, clusterName)
	if err != nil {
		return HealthResult{
			Name:    "Control Plane",
			Status:  StatusWarn,
			Score:   70,
			Message: fmt.Sprintf("Unable to fetch control-plane metrics: %v", err),
			Skipped: true,
		}
	}
	return evaluateControlPlane(m)
}

// controlPlaneQuery pairs a CloudWatch query id with its AWS/EKS metric name and
// statistic. Sum for counters (request totals), Maximum for gauges (etcd size,
// pending pods).
var controlPlaneQueries = []struct {
	id, metric, stat string
}{
	{"etcd", "etcd_mvcc_db_total_size_in_use_in_bytes", "Maximum"},
	{"req_total", "apiserver_request_total", "Sum"},
	{"req_5xx", "apiserver_request_total_5XX", "Sum"},
	{"req_429", "apiserver_request_total_429", "Sum"},
	{"pending", "scheduler_pending_pods", "Maximum"},
}

// fetchControlPlaneMetrics pulls the AWS/EKS control-plane metrics for one
// cluster in a single GetMetricData call, aggregating each over the window.
func fetchControlPlaneMetrics(ctx context.Context, api metricDataAPI, clusterName string) (controlPlaneMetrics, error) {
	end := time.Now()
	start := end.Add(-controlPlaneWindow)
	period := int32(controlPlaneWindow.Seconds())

	queries := make([]cwtypes.MetricDataQuery, 0, len(controlPlaneQueries))
	for _, q := range controlPlaneQueries {
		queries = append(queries, cwtypes.MetricDataQuery{
			Id: aws.String(q.id),
			MetricStat: &cwtypes.MetricStat{
				Metric: &cwtypes.Metric{
					Namespace:  aws.String("AWS/EKS"),
					MetricName: aws.String(q.metric),
					Dimensions: []cwtypes.Dimension{{Name: aws.String("ClusterName"), Value: aws.String(clusterName)}},
				},
				Period: aws.Int32(period),
				Stat:   aws.String(q.stat),
			},
		})
	}

	out, err := api.GetMetricData(ctx, &cloudwatch.GetMetricDataInput{
		StartTime:         aws.Time(start),
		EndTime:           aws.Time(end),
		MetricDataQueries: queries,
	})
	if err != nil {
		return controlPlaneMetrics{}, err
	}

	var m controlPlaneMetrics
	for _, r := range out.MetricDataResults {
		if r.Id == nil || len(r.Values) == 0 {
			continue
		}
		m.hasData = true
		v := aggregateValues(*r.Id, r.Values)
		switch *r.Id {
		case "etcd":
			m.etcdInUseBytes, m.etcdKnown = v, true
		case "req_total":
			m.reqTotal = v
		case "req_5xx":
			m.req5xx = v
		case "req_429":
			m.req429 = v
		case "pending":
			m.pendingPods, m.pendingKnown = v, true
		}
	}
	return m, nil
}

// aggregateValues collapses a result's datapoints to one number: the max for
// gauges, the sum for counters. With a single window-length period there is
// usually one value, but a metric can return a few; reduce defensively.
func aggregateValues(id string, values []float64) float64 {
	sumStat := id == "req_total" || id == "req_5xx" || id == "req_429"
	if sumStat {
		total := 0.0
		for _, v := range values {
			total += v
		}
		return total
	}
	return maxFloat(values)
}

// evaluateControlPlane turns the aggregated metrics into a gate verdict (pure,
// table-testable). etcd usage drives Warn/Fail; the API-server error rate is
// advisory; the scheduler backlog is informational.
func evaluateControlPlane(m controlPlaneMetrics) HealthResult {
	result := HealthResult{Name: "Control Plane", IsBlocking: true}

	if !m.hasData {
		result.Status = StatusPass
		result.Skipped = true
		result.Message = "control-plane metrics unavailable (requires EKS 1.28+ on a supported platform version)"
		return result
	}

	result.Status = StatusPass
	result.Score = 100
	result.Message = "Control plane healthy"

	// etcd usage vs the 8 GiB read-only limit — the blocking signal.
	if m.etcdKnown {
		pct := m.etcdInUseBytes / float64(etcdQuotaBytes) * 100
		result.Details = append(result.Details,
			fmt.Sprintf("etcd database %.1f%% of the 8 GiB limit (%.2f GiB in use)", pct, m.etcdInUseBytes/(1024*1024*1024)))
		switch {
		case pct >= etcdFailPercent:
			result.Status = StatusFail
			result.Score = 30
			result.Message = fmt.Sprintf("etcd database near the 8 GiB read-only limit (%.1f%%) — compact before upgrading", pct)
		case pct >= etcdWarnPercent:
			result.Status = StatusWarn
			result.Score = 70
			result.Message = fmt.Sprintf("etcd database growing toward the 8 GiB limit (%.1f%%)", pct)
		}
	} else {
		result.Details = append(result.Details, "etcd size metric not reported")
	}

	// API-server error rate — advisory (warn-only), and only above a volume
	// floor so a handful of errors on an idle cluster doesn't trip it.
	if m.reqTotal >= apiserverMinReqsForRate {
		errPct := m.req5xx / m.reqTotal * 100
		result.Details = append(result.Details,
			fmt.Sprintf("API-server 5xx rate %.2f%% (%d of %d requests)", errPct, int(m.req5xx), int(m.reqTotal)))
		if errPct >= apiserver5xxWarnPercent && result.Status == StatusPass {
			result.Status = StatusWarn
			result.Score = 70
			result.Message = fmt.Sprintf("Elevated API-server error rate (%.2f%% 5xx)", errPct)
		}
	}
	if m.req429 > 0 {
		result.Details = append(result.Details, fmt.Sprintf("API-server throttled %d requests (429) in the window", int(m.req429)))
		if result.Status == StatusPass {
			result.Status = StatusWarn
			result.Score = 70
			result.Message = "API-server is throttling requests (429)"
		}
	}

	// Scheduler backlog — informational only.
	if m.pendingKnown && m.pendingPods > 0 {
		result.Details = append(result.Details, fmt.Sprintf("Scheduler pending pods: %d", int(m.pendingPods)))
	}

	return result
}
