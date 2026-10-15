package health_test

import (
	"testing"

	"github.com/dantech2000/refresh/internal/health"
)

// TestQuotas_SurgeBlocks holds the quota check to the rule of the gate: a
// likely surge over the quota blocks, and --force goes past it with a
// warning, as it goes past DrainBlockers. A surge that only the documented
// maximum puts over the quota, and the rules of current use, ask.
func TestQuotas_SurgeBlocks(t *testing.T) {
	tests := []struct {
		name     string
		edits    []edit
		check    health.Check
		targets  []string
		policy   health.Policy
		decision health.Decision
		exit     int
		blocking bool // the Blocking of the result
		forced   bool // the reason is in Verdict.Forced
	}{
		{
			name: "a likely surge over the quota blocks", edits: []edit{quotaWorld(8, 4)}, targets: []string{"ng-a"},
			decision: health.Block, exit: 3, blocking: true,
		},
		{
			name: "--force goes past it", edits: []edit{quotaWorld(8, 4)}, targets: []string{"ng-a"},
			policy: health.Policy{Force: true}, decision: health.Ask, exit: 2, blocking: true, forced: true,
		},
		{
			name: "--force and --yes go on", edits: []edit{quotaWorld(8, 4)}, targets: []string{"ng-a"},
			policy: health.Policy{Force: true, Yes: true}, decision: health.Go, exit: 2, blocking: true, forced: true,
		},
		{
			name: "a scale-up over the quota blocks", edits: []edit{quotaWorld(8, 4)}, targets: []string{"ng-a"},
			check: health.ScaleUpQuotas("ng-a", 3), decision: health.Block, exit: 3, blocking: true,
		},
		{
			name: "--force goes past a scale-up", edits: []edit{quotaWorld(8, 4)}, targets: []string{"ng-a"},
			check: health.ScaleUpQuotas("ng-a", 3), policy: health.Policy{Force: true, Yes: true}, decision: health.Go, exit: 2,
			blocking: true, forced: true,
		},
		{
			name: "the documented maximum over the quota asks", edits: []edit{quotaWorld(12, 4)}, targets: []string{"ng-a"},
			decision: health.Ask, exit: 2, blocking: true,
		},
		{
			name: "current use at 95 percent asks", edits: []edit{quotaWorld(100, 96)},
			decision: health.Ask, exit: 2,
		},
		{
			name: "--force does not count current use as forced", edits: []edit{quotaWorld(100, 96)},
			policy: health.Policy{Force: true}, decision: health.Ask, exit: 2,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t, tt.edits...)
			c := tt.check
			if c.Run == nil {
				c = health.Quotas()
			}
			got := h.run(testContext(t), c, tt.targets...)
			if got.Blocking != tt.blocking {
				t.Errorf("Blocking = %t, want %t (%s: %s)", got.Blocking, tt.blocking, got.Outcome, got.Message)
			}
			v := health.Gate([]health.Result{got}, tt.policy)
			if v.Decision != tt.decision || v.Exit != tt.exit {
				t.Errorf("verdict = %s, exit %d; want %s, exit %d (%s: %s)", v.Decision, v.Exit, tt.decision, tt.exit, got.Outcome, got.Message)
			}
			if forced := len(v.Forced) > 0; forced != tt.forced {
				t.Errorf("forced = %v, want %t", v.Forced, tt.forced)
			}
		})
	}
}
