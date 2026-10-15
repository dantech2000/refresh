package health

import (
	"context"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	cwtypes "github.com/aws/aws-sdk-go-v2/service/cloudwatch/types"

	"github.com/dantech2000/refresh/internal/apidoc"
	"github.com/dantech2000/refresh/internal/awsx"
	"github.com/dantech2000/refresh/internal/fleet"
)

// The control-plane thresholds. etcd is the blocking signal: at 8 GiB the
// database goes read-only, which AWS documents. The API server rates only
// warn, and only above a request floor so an idle cluster does not trip
// on a handful of errors.
const (
	etcdLimitBytes      = 8 << 30
	etcdFailPercent     = 95.0
	etcdWarnPercent     = 80.0
	apiServer5xxPercent = 2.0
	apiServer429Percent = 1.0
	apiServerMinReqs    = 1000.0
	controlPlaneWindow  = 15 * time.Minute
)

// controlPlaneQueries are the AWS/EKS metrics the check reads, with the
// statistic for each: the maximum of a gauge, the sum of a counter.
var controlPlaneQueries = []struct{ id, metric, stat string }{
	{"etcd", "etcd_mvcc_db_total_size_in_use_in_bytes", "Maximum"},
	{"req_total", "apiserver_request_total", "Sum"},
	{"req_5xx", "apiserver_request_total_5XX", "Sum"},
	{"req_429", "apiserver_request_total_429", "Sum"},
	{"pending", "scheduler_pending_pods", "Maximum"},
}

// ControlPlane checks the EKS control plane from its free CloudWatch
// metrics: the etcd database against its 8 GiB limit, and the API server
// error and throttle rates over the last 15 minutes. A control plane that
// emits no metrics, which is one below 1.28, is skipped.
func ControlPlane() Check {
	return Check{Name: "ControlPlane", Blocking: true, Run: runControlPlane}
}

// controlPlaneMetrics are the metrics of the window. A Known flag tells a
// measured zero from a metric that was not emitted.
type controlPlaneMetrics struct {
	etcdBytes, reqTotal, req5xx, req429, pending float64
	etcdKnown, pendingKnown, any                 bool
}

func runControlPlane(ctx context.Context, in *Input) Result {
	cluster := in.Snapshot.Cluster.Name
	m, err := fetchControlPlaneMetrics(ctx, in, cluster)
	if err != nil {
		f := fleet.FailureFromError(apidoc.FailureCluster, cluster, err)
		return Result{Outcome: Unreadable, Message: "could not read the control-plane metrics: " + f.Error + failureAction(f), Failures: []apidoc.Failure{f}}
	}
	return evaluateControlPlane(m)
}

// failureAction names the IAM action of a failed AWS read, for a message.
func failureAction(f apidoc.Failure) string {
	if f.Operation == "" {
		return ""
	}
	return " (" + f.Operation + ")"
}

func fetchControlPlaneMetrics(ctx context.Context, in *Input, cluster string) (controlPlaneMetrics, error) {
	end := in.Now
	start := end.Add(-controlPlaneWindow)
	queries := make([]cwtypes.MetricDataQuery, 0, len(controlPlaneQueries))
	for _, q := range controlPlaneQueries {
		queries = append(queries, cwtypes.MetricDataQuery{
			Id: aws.String(q.id),
			MetricStat: &cwtypes.MetricStat{
				Metric: &cwtypes.Metric{
					Namespace:  aws.String("AWS/EKS"),
					MetricName: aws.String(q.metric),
					Dimensions: []cwtypes.Dimension{{Name: aws.String("ClusterName"), Value: aws.String(cluster)}},
				},
				Period: aws.Int32(int32(controlPlaneWindow.Seconds())),
				Stat:   aws.String(q.stat),
			},
		})
	}
	results, err := metricData(ctx, in.Clients.CloudWatch, &cloudwatch.GetMetricDataInput{
		StartTime: aws.Time(start), EndTime: aws.Time(end), MetricDataQueries: queries,
	})
	if err != nil {
		return controlPlaneMetrics{}, err
	}
	var m controlPlaneMetrics
	for _, r := range results {
		if len(r.Values) == 0 {
			continue
		}
		m.any = true
		switch aws.ToString(r.Id) {
		case "etcd":
			m.etcdBytes, m.etcdKnown = maxOf(r.Values), true
		case "req_total":
			m.reqTotal = sumOf(r.Values)
		case "req_5xx":
			m.req5xx = sumOf(r.Values)
		case "req_429":
			m.req429 = sumOf(r.Values)
		case "pending":
			m.pending, m.pendingKnown = maxOf(r.Values), true
		}
	}
	return m, nil
}

// evaluateControlPlane is the rule, apart from the read.
func evaluateControlPlane(m controlPlaneMetrics) Result {
	if !m.any {
		return Result{Outcome: Skipped, Message: "control-plane metrics not emitted (EKS 1.28+ on a supported platform version)"}
	}
	r := Result{Outcome: Pass, Message: "control plane healthy"}
	if m.etcdKnown {
		pct := m.etcdBytes / etcdLimitBytes * 100
		r.Details = append(r.Details, fmt.Sprintf("etcd database %.1f%% of the 8 GiB limit (%.2f GiB in use)", pct, m.etcdBytes/(1<<30)))
		switch {
		case pct >= etcdFailPercent:
			r.Outcome, r.Message = Fail, fmt.Sprintf("etcd database near the 8 GiB read-only limit (%.1f%%); compact it before a change", pct)
		case pct >= etcdWarnPercent:
			r.Outcome, r.Message = Warn, fmt.Sprintf("etcd database growing toward the 8 GiB limit (%.1f%%)", pct)
		default:
			r.Message = fmt.Sprintf("etcd %.0f%% of 8 GiB", pct)
		}
	} else {
		r.Details = append(r.Details, "etcd size metric not reported")
	}
	if m.reqTotal >= apiServerMinReqs {
		errPct := m.req5xx / m.reqTotal * 100
		r.Details = append(r.Details, fmt.Sprintf("API server 5xx rate %.2f%% (%.0f of %.0f requests)", errPct, m.req5xx, m.reqTotal))
		if errPct >= apiServer5xxPercent && r.Outcome == Pass {
			r.Outcome, r.Message = Warn, fmt.Sprintf("elevated API server error rate (%.2f%% 5xx)", errPct)
		}
		if throttled := m.req429 / m.reqTotal * 100; throttled >= apiServer429Percent && r.Outcome == Pass {
			r.Outcome, r.Message = Warn, fmt.Sprintf("API server is throttling requests (%.1f%% 429)", throttled)
		}
	}
	if m.req429 > 0 {
		r.Details = append(r.Details, fmt.Sprintf("API server throttled %.0f requests (429) in the window", m.req429))
	}
	if m.pendingKnown && m.pending > 0 {
		r.Details = append(r.Details, fmt.Sprintf("scheduler pending pods: %.0f", m.pending))
	}
	return r
}

// metricData reads every page of one GetMetricData call.
func metricData(ctx context.Context, cw *cloudwatch.Client, in *cloudwatch.GetMetricDataInput) ([]cwtypes.MetricDataResult, error) {
	return awsx.ListAll(ctx,
		func(ctx context.Context, token *string) (*cloudwatch.GetMetricDataOutput, error) {
			in.NextToken = token
			return cw.GetMetricData(ctx, in)
		},
		func(out *cloudwatch.GetMetricDataOutput) ([]cwtypes.MetricDataResult, *string) {
			return out.MetricDataResults, out.NextToken
		},
	)
}

func sumOf(values []float64) float64 {
	total := 0.0
	for _, v := range values {
		total += v
	}
	return total
}

func maxOf(values []float64) float64 {
	m := 0.0
	for i, v := range values {
		if i == 0 || v > m {
			m = v
		}
	}
	return m
}
