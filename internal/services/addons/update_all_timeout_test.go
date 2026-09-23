package addons

import (
	"context"
	"strings"
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

// Regression: with --parallel, when the UpdateAll deadline fires before every
// add-on was dispatched, the undispatched add-ons must still come back as
// named FAILED rows (never zero-valued blank rows).
func TestUpdateAll_ParallelDeadlineFillsUndispatched(t *testing.T) {
	names := []string{"a1", "a2", "a3", "a4", "a5", "a6", "a7", "a8", "a9"}
	b := mocks.NewEKSAPI().WithCluster("prod", "1.32")
	for _, n := range names {
		b = b.WithAddon(n, "v1.0.0", ekstypes.AddonStatusActive).
			WithAddonVersions(n, []string{"v1.1.0"}, "1.32")
	}
	m := b.Build()
	// UpdateAddon blocks until the run's deadline, then fails the way the SDK
	// does when its context ends.
	m.UpdateAddonFn = func(ctx context.Context, _ *eks.UpdateAddonInput, _ ...func(*eks.Options)) (*eks.UpdateAddonOutput, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}

	svc := NewService(m, logger())
	results, err := svc.UpdateAll(context.Background(), "prod", UpdateAllOptions{
		Parallel: true,
		Timeout:  200 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("UpdateAll = %v", err)
	}
	if len(results) != len(names) {
		t.Fatalf("results = %d, want %d", len(results), len(names))
	}
	seen := map[string]bool{}
	notAttempted := 0
	for i, r := range results {
		if r.AddonName == "" {
			t.Errorf("result %d is a blank row: %+v", i, r)
			continue
		}
		seen[r.AddonName] = true
		if !strings.HasPrefix(r.Status, "FAILED") {
			t.Errorf("%s: Status = %q, want FAILED", r.AddonName, r.Status)
		}
		if r.PreviousVersion != "v1.0.0" {
			t.Errorf("%s: PreviousVersion = %q, want v1.0.0", r.AddonName, r.PreviousVersion)
		}
		if strings.Contains(r.Status, "not attempted") {
			notAttempted++
		}
	}
	if len(seen) != len(names) {
		t.Errorf("distinct add-ons = %d, want %d", len(seen), len(names))
	}
	// Only maxParallelAddonUpdates can be in flight before the deadline.
	if want := len(names) - maxParallelAddonUpdates; notAttempted != want {
		t.Errorf("not attempted = %d, want %d", notAttempted, want)
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

	var deadlines []time.Time
	m.UpdateAddonFn = func(ctx context.Context, _ *eks.UpdateAddonInput, _ ...func(*eks.Options)) (*eks.UpdateAddonOutput, error) {
		dl, ok := ctx.Deadline()
		if !ok {
			t.Error("UpdateAddon ran with no deadline")
		}
		deadlines = append(deadlines, dl)
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
	end := time.Now()
	if err != nil {
		t.Fatalf("UpdateAll = %v", err)
	}
	if len(deadlines) != 3 {
		t.Fatalf("UpdateAddon calls = %d, want 3", len(deadlines))
	}
	// The run gets exactly Timeout + 3×WaitTimeout = 25m, set once when the
	// run starts: the deadline must fall in [start+25m, end+25m], and every
	// add-on shares it. A budget that is too small or too large (say,
	// WaitTimeout counted twice) lands outside the window.
	const budget = 10*time.Minute + 3*5*time.Minute
	for i, dl := range deadlines {
		if dl.Before(start.Add(budget)) || dl.After(end.Add(budget)) {
			t.Errorf("call %d: deadline %v after start, want exactly %v (window %v)", i, dl.Sub(start), budget, end.Sub(start))
		}
		if !dl.Equal(deadlines[0]) {
			t.Errorf("call %d: deadline differs from call 0; the budget must be one deadline for the whole run", i)
		}
	}
}
