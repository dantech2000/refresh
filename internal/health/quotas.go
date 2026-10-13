package health

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	cwtypes "github.com/aws/aws-sdk-go-v2/service/cloudwatch/types"
	"github.com/aws/aws-sdk-go-v2/service/servicequotas"

	"github.com/dantech2000/refresh/internal/apidoc"
	"github.com/dantech2000/refresh/internal/awsx"
	"github.com/dantech2000/refresh/internal/fleet"
)

// The EC2 On-Demand Standard vCPU quota, which a roll surges against, and
// the usage thresholds.
const (
	quotaServiceCode = "ec2"
	quotaCode        = "L-1216C47A"
	quotaWarnPercent = 85.0
	quotaFailPercent = 95.0
	quotaUsageWindow = 10 * time.Minute
)

// Quotas checks the headroom of the EC2 On-Demand vCPU quota: the limit
// from Service Quotas, and the use of the newest minute from the AWS/Usage
// metric. It is advisory.
func Quotas() Check {
	return Check{Name: "Quotas", Run: runQuotas}
}

func runQuotas(ctx context.Context, in *Input) Result {
	var (
		wg               sync.WaitGroup
		limit, usage     float64
		haveLimit, used  bool
		limitErr, useErr error
	)
	wg.Go(func() { limit, haveLimit, limitErr = quotaLimit(ctx, in) })
	wg.Go(func() { usage, used, useErr = quotaUsage(ctx, in) })
	wg.Wait()
	cluster := in.Snapshot.Cluster.Name
	for _, err := range []error{limitErr, useErr} {
		if err == nil {
			continue
		}
		f := fleet.FailureFromError(apidoc.FailureCluster, cluster, err)
		return Result{Outcome: Unreadable, Message: "could not read the vCPU quota: " + f.Error + failureAction(f), Failures: []apidoc.Failure{f}}
	}
	switch {
	case !haveLimit || limit <= 0:
		return Result{Outcome: Skipped, Message: "the EC2 vCPU quota is not reported"}
	case !used:
		return Result{Outcome: Skipped, Message: "the AWS/Usage vCPU metric is not reported"}
	}
	return evaluateQuota(usage, limit)
}

// quotaLimit reads the quota. A quota that Service Quotas does not know
// is not an error: there is nothing to compare with.
func quotaLimit(ctx context.Context, in *Input) (float64, bool, error) {
	out, err := in.Clients.ServiceQuotas.GetServiceQuota(ctx, &servicequotas.GetServiceQuotaInput{
		ServiceCode: aws.String(quotaServiceCode), QuotaCode: aws.String(quotaCode),
	})
	if err != nil {
		if awsx.IsNotFound(err) {
			return 0, false, nil
		}
		return 0, false, err
	}
	if out.Quota == nil || out.Quota.Value == nil {
		return 0, false, nil
	}
	return *out.Quota.Value, true, nil
}

// quotaUsage reads the vCPUs in use in the newest minute of the window.
// The maximum over the window counted a surge that had already ended.
func quotaUsage(ctx context.Context, in *Input) (float64, bool, error) {
	end := in.Now
	results, err := metricData(ctx, in.Clients.CloudWatch, &cloudwatch.GetMetricDataInput{
		StartTime: aws.Time(end.Add(-quotaUsageWindow)), EndTime: aws.Time(end),
		ScanBy: cwtypes.ScanByTimestampDescending,
		MetricDataQueries: []cwtypes.MetricDataQuery{{
			Id: aws.String("vcpu"),
			MetricStat: &cwtypes.MetricStat{
				Metric: &cwtypes.Metric{
					Namespace:  aws.String("AWS/Usage"),
					MetricName: aws.String("ResourceCount"),
					Dimensions: []cwtypes.Dimension{
						{Name: aws.String("Service"), Value: aws.String("EC2")},
						{Name: aws.String("Type"), Value: aws.String("Resource")},
						{Name: aws.String("Resource"), Value: aws.String("vCPU")},
						{Name: aws.String("Class"), Value: aws.String("Standard/OnDemand")},
					},
				},
				Period: aws.Int32(60),
				Stat:   aws.String("Maximum"),
			},
		}},
	})
	if err != nil {
		return 0, false, err
	}
	for _, r := range results {
		if aws.ToString(r.Id) == "vcpu" && len(r.Values) > 0 {
			return r.Values[0], true, nil
		}
	}
	return 0, false, nil
}

// evaluateQuota is the rule, apart from the reads.
func evaluateQuota(usage, limit float64) Result {
	pct := usage / limit * 100
	r := Result{Details: []string{fmt.Sprintf("EC2 On-Demand Standard vCPUs: %.0f of %.0f in use (%.1f%%, %.0f free)", usage, limit, pct, limit-usage)}}
	switch {
	case pct >= quotaFailPercent:
		r.Outcome, r.Message = Fail, fmt.Sprintf("EC2 vCPU quota nearly exhausted (%.0f%% in use): a roll may fail to launch nodes", pct)
	case pct >= quotaWarnPercent:
		r.Outcome, r.Message = Warn, fmt.Sprintf("%.0f%% of the vCPU quota in use", pct)
	default:
		r.Outcome, r.Message = Pass, fmt.Sprintf("%.0f%% of the vCPU quota in use (%.0f of %.0f)", pct, usage, limit)
	}
	return r
}
