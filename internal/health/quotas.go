package health

import (
	"context"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	cwtypes "github.com/aws/aws-sdk-go-v2/service/cloudwatch/types"
	"github.com/aws/aws-sdk-go-v2/service/servicequotas"
)

// EC2 On-Demand Standard (A, C, D, H, I, M, R, T, Z) vCPU quota — the limit a
// nodegroup scale-up or AMI roll (which surges new nodes) consumes against.
const (
	ec2ServiceCode      = "ec2"
	onDemandVCPUQuota   = "L-1216C47A"
	quotaWarnPercent    = 85.0
	quotaHighPercent    = 95.0
	quotaUsageWindow    = 10 * time.Minute
	quotaUsageNamespace = "AWS/Usage"
)

// serviceQuotaAPI is the slice of Service Quotas the headroom check needs.
type serviceQuotaAPI interface {
	GetServiceQuota(ctx context.Context, in *servicequotas.GetServiceQuotaInput, optFns ...func(*servicequotas.Options)) (*servicequotas.GetServiceQuotaOutput, error)
}

// SetServiceQuotas attaches a Service Quotas client, enabling the vCPU quota
// headroom check. Without it (and a CloudWatch client), the check is skipped.
func (hc *HealthChecker) SetServiceQuotas(sq serviceQuotaAPI) { hc.sqClient = sq }

// CheckServiceQuotas reports EC2 On-Demand vCPU usage against the account quota
// — the headroom for adding nodes during a scale-up or roll. The limit comes
// from Service Quotas; current usage from the AWS/Usage CloudWatch metric (the
// quota API returns only the limit, not usage). Advisory (non-blocking); skips
// when either client is missing or the limit/usage can't be read.
func (hc *HealthChecker) CheckServiceQuotas(ctx context.Context, _ string) HealthResult {
	if hc.sqClient == nil || hc.cwClient == nil {
		return HealthResult{Name: "Service Quotas", Status: StatusPass, Skipped: true,
			Message: "service-quota headroom unavailable (clients not configured)"}
	}
	return checkServiceQuotas(ctx, hc.sqClient, hc.cwClient)
}

// checkServiceQuotas is CheckServiceQuotas against injectable clients (testable).
func checkServiceQuotas(ctx context.Context, sq serviceQuotaAPI, md metricDataAPI) HealthResult {
	limit, err := onDemandVCPULimit(ctx, sq)
	if err != nil {
		return HealthResult{Name: "Service Quotas", Status: StatusPass, Skipped: true,
			Message: fmt.Sprintf("service-quota headroom unavailable: %v", err)}
	}
	usage, ok, err := onDemandVCPUUsage(ctx, md)
	if err != nil || !ok {
		return HealthResult{Name: "Service Quotas", Status: StatusPass, Skipped: true,
			Message: "service-quota usage unavailable (AWS/Usage metric not reported)"}
	}
	return evaluateQuota(usage, limit)
}

func onDemandVCPULimit(ctx context.Context, sq serviceQuotaAPI) (float64, error) {
	out, err := sq.GetServiceQuota(ctx, &servicequotas.GetServiceQuotaInput{
		ServiceCode: aws.String(ec2ServiceCode),
		QuotaCode:   aws.String(onDemandVCPUQuota),
	})
	if err != nil {
		return 0, err
	}
	if out == nil || out.Quota == nil || out.Quota.Value == nil {
		return 0, fmt.Errorf("no quota value returned")
	}
	return *out.Quota.Value, nil
}

func onDemandVCPUUsage(ctx context.Context, md metricDataAPI) (usage float64, ok bool, err error) {
	end := time.Now()
	start := end.Add(-quotaUsageWindow)
	out, err := md.GetMetricData(ctx, &cloudwatch.GetMetricDataInput{
		StartTime: aws.Time(start),
		EndTime:   aws.Time(end),
		MetricDataQueries: []cwtypes.MetricDataQuery{{
			Id: aws.String("vcpu"),
			MetricStat: &cwtypes.MetricStat{
				Metric: &cwtypes.Metric{
					Namespace:  aws.String(quotaUsageNamespace),
					MetricName: aws.String("ResourceCount"),
					Dimensions: []cwtypes.Dimension{
						{Name: aws.String("Service"), Value: aws.String("EC2")},
						{Name: aws.String("Type"), Value: aws.String("Resource")},
						{Name: aws.String("Resource"), Value: aws.String("vCPU")},
						{Name: aws.String("Class"), Value: aws.String("Standard/OnDemand")},
					},
				},
				Period: aws.Int32(int32(quotaUsageWindow.Seconds())),
				Stat:   aws.String("Maximum"),
			},
		}},
	})
	if err != nil {
		return 0, false, err
	}
	for _, r := range out.MetricDataResults {
		if aws.ToString(r.Id) == "vcpu" && len(r.Values) > 0 {
			return maxFloat(r.Values), true, nil
		}
	}
	return 0, false, nil
}

// evaluateQuota turns usage vs limit into an advisory verdict (pure, testable).
func evaluateQuota(usage, limit float64) HealthResult {
	r := HealthResult{Name: "Service Quotas", IsBlocking: false, Status: StatusPass, Score: 100}
	if limit <= 0 {
		r.Skipped = true
		r.Message = "service-quota limit unavailable"
		return r
	}
	pct := usage / limit * 100
	r.Details = append(r.Details, fmt.Sprintf("EC2 On-Demand Standard vCPUs: %.0f of %.0f used (%.1f%%, %.0f free)", usage, limit, pct, limit-usage))
	switch {
	case pct >= quotaHighPercent:
		r.Status = StatusFail
		r.Score = 40
		r.Message = fmt.Sprintf("EC2 vCPU quota nearly exhausted (%.1f%%) — a scale-up or roll may fail to launch nodes", pct)
	case pct >= quotaWarnPercent:
		r.Status = StatusWarn
		r.Score = 70
		r.Message = fmt.Sprintf("EC2 vCPU quota usage high (%.1f%%) — limited headroom for new nodes", pct)
	default:
		r.Message = fmt.Sprintf("EC2 vCPU quota headroom healthy (%.1f%% used)", pct)
	}
	return r
}
