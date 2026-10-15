package health_test

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dantech2000/refresh/internal/fleet/fleettest"
	"github.com/dantech2000/refresh/internal/health"
)

// staleUsage is instanceWorld with an AWS/Usage metric whose newest data
// point is lag old and counts value vCPUs: 2 t4g.small instances (4 vCPU)
// run, and the metric may still count a surge node that has ended.
func staleUsage(quota, value float64, lag time.Duration) edit {
	return func(s *fleettest.State, c *fleettest.Cluster) {
		instanceWorld(quota)(s, c)
		m := usageMetric(value)
		m.Lag = lag
		s.Metrics = append(s.Metrics, m)
	}
}

// TestQuotas_StaleMetric counts the instance list next to an AWS/Usage
// metric whose newest data point is old, and takes the smaller of the two:
// the metric lags, and never counts fewer vCPUs than the instances that
// run. Right after a roll the metric said 6 of 8 vCPUs while 2 t4g.small
// instances (4 vCPU) ran, and a scale-up by 2 t3.medium nodes (4 vCPU)
// that fits was reported as one that does not.
func TestQuotas_StaleMetric(t *testing.T) {
	tests := []struct {
		name   string
		edit   edit
		want   want
		detail string
		// lists says whether the check reads the instance list.
		lists bool
	}{
		{
			name: "a stale metric that still counts the surge",
			edit: staleUsage(8, 6, 6*time.Minute),
			want: want{outcome: health.Pass, message: "50% of the vCPU quota in use (4 of 8)"},
			detail: "the vCPUs in use come from the instance list (4), not from the AWS/Usage metric (6): " +
				"the newest data point of the metric is 6m0s old",
			lists: true,
		},
		{
			name:   "a stale metric below the instance list",
			edit:   staleUsage(8, 2, 6*time.Minute),
			want:   want{outcome: health.Pass, message: "25% of the vCPU quota in use (2 of 8)"},
			detail: "the vCPUs in use come from the AWS/Usage metric (2): its newest data point is 6m0s old, and the instance list counts at least 4",
			lists:  true,
		},
		{
			name: "a fresh metric decides alone",
			edit: staleUsage(8, 6, 0),
			want: want{outcome: health.Fail, message: "scaling ng-a up by 2 launches 2 t3.medium nodes (4 vCPU) and 6 of 8 vCPU are in use"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t, tt.edit)
			ctx := testContext(t)
			in := h.input(ctx, "ng-a")
			before := countAWS(h.aws, fleettest.EC2DescribeInstances)
			got := health.Run(ctx, []health.Check{health.ScaleUpQuotas("ng-a", 2)}, in)[0]
			check(t, got, tt.want)
			if tt.detail != "" && !slices.ContainsFunc(got.Details, func(d string) bool { return strings.Contains(d, tt.detail) }) {
				t.Errorf("details = %q, want one with %q", got.Details, tt.detail)
			}
			if listed := countAWS(h.aws, fleettest.EC2DescribeInstances) > before; listed != tt.lists {
				t.Errorf("read the instance list = %t, want %t", listed, tt.lists)
			}
		})
	}
	t.Run("the instance list fails", func(t *testing.T) {
		h := newHarness(t, staleUsage(8, 6, 6*time.Minute))
		ctx := testContext(t)
		in := h.input(ctx, "ng-a")
		h.world.SetFault(fleettest.EC2DescribeInstances, fleettest.Fault{Deny: true})
		got := health.Run(ctx, []health.Check{health.ScaleUpQuotas("ng-a", 2)}, in)[0]
		check(t, got, want{outcome: health.Fail, message: "6 of 8 vCPU are in use"})
		const detail = "the newest data point of the AWS/Usage metric is 6m0s old, and the instance list could not be read"
		if !slices.ContainsFunc(got.Details, func(d string) bool { return strings.Contains(d, detail) }) {
			t.Errorf("details = %q, want one with %q", got.Details, detail)
		}
	})
}
