package health

import (
	"context"
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	cwtypes "github.com/aws/aws-sdk-go-v2/service/cloudwatch/types"
	"github.com/aws/aws-sdk-go-v2/service/servicequotas"
	sqtypes "github.com/aws/aws-sdk-go-v2/service/servicequotas/types"
)

type fakeServiceQuotas struct {
	value *float64
	err   error
}

func (f *fakeServiceQuotas) GetServiceQuota(_ context.Context, _ *servicequotas.GetServiceQuotaInput, _ ...func(*servicequotas.Options)) (*servicequotas.GetServiceQuotaOutput, error) {
	if f.err != nil {
		return nil, f.err
	}
	return &servicequotas.GetServiceQuotaOutput{Quota: &sqtypes.ServiceQuota{Value: f.value}}, nil
}

// usageMetrics is a metricDataAPI stub returning a fixed vCPU usage value.
type usageMetrics struct {
	value   float64
	hasData bool
	err     error
}

func (u *usageMetrics) GetMetricData(_ context.Context, _ *cloudwatch.GetMetricDataInput, _ ...func(*cloudwatch.Options)) (*cloudwatch.GetMetricDataOutput, error) {
	if u.err != nil {
		return nil, u.err
	}
	res := cwtypes.MetricDataResult{Id: aws.String("vcpu")}
	if u.hasData {
		res.Values = []float64{u.value}
	}
	return &cloudwatch.GetMetricDataOutput{MetricDataResults: []cwtypes.MetricDataResult{res}}, nil
}

func TestEvaluateQuota(t *testing.T) {
	tests := []struct {
		name         string
		usage, limit float64
		wantStatus   HealthStatus
		wantSkipped  bool
	}{
		{"healthy", 100, 1000, StatusPass, false},
		{"high warns", 880, 1000, StatusWarn, false}, // 88%
		{"near-exhausted fails", 970, 1000, StatusFail, false},
		{"no limit skipped", 10, 0, StatusPass, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := evaluateQuota(tc.usage, tc.limit)
			if r.Status != tc.wantStatus || r.Skipped != tc.wantSkipped {
				t.Errorf("got status=%s skipped=%v, want status=%s skipped=%v (%+v)", r.Status, r.Skipped, tc.wantStatus, tc.wantSkipped, r)
			}
			if r.IsBlocking {
				t.Error("quota check should be advisory (non-blocking)")
			}
		})
	}
}

func TestCheckServiceQuotas_FetchAndEvaluate(t *testing.T) {
	sq := &fakeServiceQuotas{value: aws.Float64(1000)}
	md := &usageMetrics{value: 970, hasData: true} // 97% → near-exhausted → fail (advisory)

	r := checkServiceQuotas(context.Background(), sq, md)
	if r.Skipped {
		t.Fatalf("expected a measured result, got skipped: %+v", r)
	}
	if r.Status != StatusFail {
		t.Errorf("status = %s, want FAIL at 97%% (%+v)", r.Status, r)
	}
	if len(r.Details) == 0 {
		t.Error("expected usage detail line")
	}
}

func TestCheckServiceQuotas_SkipsGracefully(t *testing.T) {
	// Quota fetch error → skip.
	if r := checkServiceQuotas(context.Background(), &fakeServiceQuotas{err: errors.New("AccessDenied")}, &usageMetrics{value: 1, hasData: true}); !r.Skipped {
		t.Errorf("quota error should skip, got %+v", r)
	}
	// Usage metric absent (AWS/Usage not reported) → skip, not fail.
	if r := checkServiceQuotas(context.Background(), &fakeServiceQuotas{value: aws.Float64(1000)}, &usageMetrics{hasData: false}); !r.Skipped || r.Status == StatusFail {
		t.Errorf("missing usage should skip (not fail), got %+v", r)
	}
}

// scriptedUsage returns the values it is given, newest first, and records the
// query so a test can check it asked for recent minutes, newest first.
type scriptedUsage struct {
	values []float64
	in     *cloudwatch.GetMetricDataInput
}

func (s *scriptedUsage) GetMetricData(_ context.Context, in *cloudwatch.GetMetricDataInput, _ ...func(*cloudwatch.Options)) (*cloudwatch.GetMetricDataOutput, error) {
	s.in = in
	return &cloudwatch.GetMetricDataOutput{MetricDataResults: []cwtypes.MetricDataResult{{Id: aws.String("vcpu"), Values: s.values}}}, nil
}

// On a real account with an 8 vCPU quota, a roll surged 2 → 4 t3.medium nodes
// and back. Just after it, the minutes read 4, 6, 8, 8 (newest first): the
// check must see the current 4 (50%), not the finished surge's 8 (100%).
func TestCheckServiceQuotas_UsesTheNewestMinute(t *testing.T) {
	md := &scriptedUsage{values: []float64{4, 6, 8, 8}}
	r := checkServiceQuotas(context.Background(), &fakeServiceQuotas{value: aws.Float64(8)}, md)
	if r.Status != StatusPass || r.Skipped {
		t.Fatalf("result = %+v, want PASS at 50%%", r)
	}
	if md.in.ScanBy != cwtypes.ScanByTimestampDescending || aws.ToInt32(md.in.MetricDataQueries[0].MetricStat.Period) != 60 {
		t.Errorf("query = scan %s, period %d; want newest first, per minute", md.in.ScanBy, aws.ToInt32(md.in.MetricDataQueries[0].MetricStat.Period))
	}
}
