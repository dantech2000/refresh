package addons

import (
	"context"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"

	"github.com/dantech2000/refresh/internal/mocks"
)

func TestUpdateAllBudget(t *testing.T) {
	cases := []struct {
		name string
		opts UpdateAllOptions
		n    int
		want time.Duration
	}{
		{"no timeout", UpdateAllOptions{Wait: true, WaitTimeout: 5 * time.Minute}, 4, 0},
		{"no wait", UpdateAllOptions{Timeout: 10 * time.Minute}, 4, 10 * time.Minute},
		{"serial wait scales by count", UpdateAllOptions{Timeout: 10 * time.Minute, Wait: true, WaitTimeout: 5 * time.Minute}, 4, 30 * time.Minute},
		{"parallel wait scales by batch", UpdateAllOptions{Timeout: 10 * time.Minute, Wait: true, WaitTimeout: 5 * time.Minute, Parallel: true}, 4, 20 * time.Minute},
		{"wait without wait-timeout", UpdateAllOptions{Timeout: 10 * time.Minute, Wait: true}, 4, 10 * time.Minute},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := updateAllBudget(tc.opts, tc.n); got != tc.want {
				t.Errorf("updateAllBudget = %v, want %v", got, tc.want)
			}
		})
	}
}

// Regression: with --all --wait, the overall --timeout must not truncate the
// per-add-on waits. The update context's deadline must cover Timeout plus
// WaitTimeout for every add-on.
func TestUpdateAll_WaitDeadlineScalesWithAddonCount(t *testing.T) {
	m := mocks.NewEKSAPI().
		WithCluster("prod", "1.32").
		WithAddon("vpc-cni", "v1.18.0", ekstypes.AddonStatusActive).
		WithAddon("coredns", "v1.11.1", ekstypes.AddonStatusActive).
		WithAddon("kube-proxy", "v1.32.0", ekstypes.AddonStatusActive).
		WithAddonVersions("vpc-cni", []string{"v1.19.0"}, "1.32").
		WithAddonVersions("coredns", []string{"v1.11.3"}, "1.32").
		WithAddonVersions("kube-proxy", []string{"v1.32.1"}, "1.32").
		Build()

	var deadlines []time.Duration
	m.UpdateAddonFn = func(ctx context.Context, _ *eks.UpdateAddonInput, _ ...func(*eks.Options)) (*eks.UpdateAddonOutput, error) {
		if dl, ok := ctx.Deadline(); ok {
			deadlines = append(deadlines, time.Until(dl))
		} else {
			deadlines = append(deadlines, -1)
		}
		// Fail fast (non-retryable) so no wait polling happens in the test.
		return nil, &ekstypes.InvalidParameterException{Message: aws.String("stop")}
	}

	svc := NewService(m, logger())
	start := time.Now()
	_, err := svc.UpdateAll(context.Background(), "prod", UpdateAllOptions{
		Timeout:     10 * time.Minute,
		Wait:        true,
		WaitTimeout: 5 * time.Minute,
	})
	if err != nil {
		t.Fatalf("UpdateAll = %v", err)
	}
	if len(deadlines) != 3 {
		t.Fatalf("UpdateAddon calls = %d, want 3", len(deadlines))
	}
	// 10m + 3×5m = 25m; allow for elapsed test time.
	want := 25*time.Minute - time.Since(start) - time.Second
	for i, d := range deadlines {
		if d < want {
			t.Errorf("call %d: deadline in %v, want >= ~25m (was the overall timeout applied unscaled?)", i, d)
		}
	}
}
