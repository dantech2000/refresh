package nodegroup

import "testing"

func TestFleetExit_WorstOutcome(t *testing.T) {
	cases := []struct {
		name    string
		results []clusterUpdateResult
		want    int
	}{
		{"all clean", []clusterUpdateResult{{Outcomes: updateOutcomes{Started: []string{"a"}}}}, 0},
		{"health blocked", []clusterUpdateResult{{HealthBlocked: true, Error: "block"}}, 3},
		{"update failed", []clusterUpdateResult{{Outcomes: updateOutcomes{Failed: []string{"a"}}}}, 4},
		{"verify failed", []clusterUpdateResult{{VerifyFailed: true, Outcomes: updateOutcomes{Started: []string{"a"}}}}, 5},
		{
			"worst wins (block + verify → 5)",
			[]clusterUpdateResult{
				{HealthBlocked: true, Error: "x"},
				{VerifyFailed: true, Outcomes: updateOutcomes{Started: []string{"a"}}},
			},
			5,
		},
		{
			"error counts as 4",
			[]clusterUpdateResult{{Error: "monitor boom"}},
			4,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := exitCodeOf(fleetExit(tc.results)); got != tc.want {
				t.Errorf("fleetExit = %d, want %d", got, tc.want)
			}
		})
	}
}
