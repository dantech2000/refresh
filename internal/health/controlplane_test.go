package health

import (
	"context"
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	cwtypes "github.com/aws/aws-sdk-go-v2/service/cloudwatch/types"
)

// fakeMetricsAPI is a GetMetricData stub that records the request and returns a
// canned response.
type fakeMetricsAPI struct {
	in  *cloudwatch.GetMetricDataInput
	out *cloudwatch.GetMetricDataOutput
	err error
}

func (f *fakeMetricsAPI) GetMetricData(_ context.Context, in *cloudwatch.GetMetricDataInput, _ ...func(*cloudwatch.Options)) (*cloudwatch.GetMetricDataOutput, error) {
	f.in = in
	return f.out, f.err
}

func gib(f float64) float64 { return f * 1024 * 1024 * 1024 }

// TestFetchControlPlaneMetrics_ParsesAndScopes verifies the AWS/EKS query is
// scoped by the ClusterName dimension and that counters are summed while gauges
// take the max.
func TestFetchControlPlaneMetrics_ParsesAndScopes(t *testing.T) {
	fake := &fakeMetricsAPI{out: &cloudwatch.GetMetricDataOutput{
		MetricDataResults: []cwtypes.MetricDataResult{
			{Id: aws.String("etcd"), Values: []float64{gib(4.2)}},
			{Id: aws.String("req_total"), Values: []float64{3000, 2000}}, // summed → 5000
			{Id: aws.String("req_5xx"), Values: []float64{100, 50}},      // summed → 150
			{Id: aws.String("req_429"), Values: []float64{0}},
			{Id: aws.String("pending"), Values: []float64{2, 7, 3}}, // max → 7
		},
	}}

	m, err := fetchControlPlaneMetrics(context.Background(), fake, "prod")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !m.hasData || !m.etcdKnown || !m.pendingKnown {
		t.Fatalf("expected data/known flags set: %+v", m)
	}
	if m.etcdInUseBytes != gib(4.2) {
		t.Errorf("etcd = %v, want %v", m.etcdInUseBytes, gib(4.2))
	}
	if m.reqTotal != 5000 || m.req5xx != 150 {
		t.Errorf("counters summed wrong: total=%v 5xx=%v", m.reqTotal, m.req5xx)
	}
	if m.pendingPods != 7 {
		t.Errorf("pending pods (max) = %v, want 7", m.pendingPods)
	}

	// Request scoping: AWS/EKS namespace + ClusterName dimension, one query each.
	if fake.in == nil || len(fake.in.MetricDataQueries) != len(controlPlaneQueries) {
		t.Fatalf("expected %d queries, got %v", len(controlPlaneQueries), fake.in)
	}
	q0 := fake.in.MetricDataQueries[0].MetricStat.Metric
	if aws.ToString(q0.Namespace) != "AWS/EKS" {
		t.Errorf("namespace = %q, want AWS/EKS", aws.ToString(q0.Namespace))
	}
	if len(q0.Dimensions) != 1 || aws.ToString(q0.Dimensions[0].Name) != "ClusterName" || aws.ToString(q0.Dimensions[0].Value) != "prod" {
		t.Errorf("dimension = %+v, want ClusterName=prod", q0.Dimensions)
	}
}

// TestCheckControlPlaneMetrics_NoData verifies a cluster not emitting the
// AWS/EKS metrics (e.g. <1.28) is Skipped, not failed.
func TestCheckControlPlaneMetrics_NoData(t *testing.T) {
	fake := &fakeMetricsAPI{out: &cloudwatch.GetMetricDataOutput{}}
	r := checkControlPlaneMetrics(context.Background(), fake, "prod")
	if !r.Skipped || r.Status == StatusFail {
		t.Errorf("no-data check should be skipped/non-fail, got %+v", r)
	}
}

// TestCheckControlPlaneMetrics_FetchError degrades to Skipped (never blocks an
// upgrade because CloudWatch hiccuped).
func TestCheckControlPlaneMetrics_FetchError(t *testing.T) {
	fake := &fakeMetricsAPI{err: errors.New("throttled")}
	r := checkControlPlaneMetrics(context.Background(), fake, "prod")
	if !r.Skipped || r.Status == StatusFail {
		t.Errorf("fetch error should be skipped/non-fail, got %+v", r)
	}
}

func TestEvaluateControlPlane(t *testing.T) {
	tests := []struct {
		name        string
		m           controlPlaneMetrics
		wantStatus  HealthStatus
		wantSkipped bool
	}{
		{
			name:       "healthy",
			m:          controlPlaneMetrics{hasData: true, etcdKnown: true, etcdInUseBytes: gib(4), reqTotal: 5000, req5xx: 0},
			wantStatus: StatusPass,
		},
		{
			name:       "etcd warn",
			m:          controlPlaneMetrics{hasData: true, etcdKnown: true, etcdInUseBytes: gib(6.6)}, // 82.5%
			wantStatus: StatusWarn,
		},
		{
			name:       "etcd fail near read-only",
			m:          controlPlaneMetrics{hasData: true, etcdKnown: true, etcdInUseBytes: gib(7.7)}, // 96.25%
			wantStatus: StatusFail,
		},
		{
			name:       "elevated 5xx warns",
			m:          controlPlaneMetrics{hasData: true, etcdKnown: true, etcdInUseBytes: gib(2), reqTotal: 5000, req5xx: 150}, // 3%
			wantStatus: StatusWarn,
		},
		{
			name:       "5xx below volume floor ignored",
			m:          controlPlaneMetrics{hasData: true, etcdKnown: true, etcdInUseBytes: gib(2), reqTotal: 500, req5xx: 100}, // 20% but < floor
			wantStatus: StatusPass,
		},
		{
			name:       "throttling warns",
			m:          controlPlaneMetrics{hasData: true, etcdKnown: true, etcdInUseBytes: gib(2), req429: 12},
			wantStatus: StatusWarn,
		},
		{
			name:        "no data skipped",
			m:           controlPlaneMetrics{hasData: false},
			wantStatus:  StatusPass,
			wantSkipped: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := evaluateControlPlane(tc.m)
			if r.Status != tc.wantStatus {
				t.Errorf("status = %s, want %s (%+v)", r.Status, tc.wantStatus, r)
			}
			if r.Skipped != tc.wantSkipped {
				t.Errorf("skipped = %v, want %v", r.Skipped, tc.wantSkipped)
			}
			// The etcd-critical case must be blocking so the gate actually holds.
			if tc.name == "etcd fail near read-only" && !r.IsBlocking {
				t.Error("etcd-near-limit failure must be blocking")
			}
		})
	}
}
