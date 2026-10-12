package addon

import (
	"testing"
	"time"
)

// --wait-timeout 0 is documented as "no limit": the run gets no deadline,
// instead of the --timeout meant for the API calls.
func TestUpdateSetupTimeout(t *testing.T) {
	cases := []struct {
		name      string
		api, wait time.Duration
		waitFlag  bool
		want      time.Duration
	}{
		{"no wait", 30 * time.Second, 5 * time.Minute, false, 30 * time.Second},
		{"wait adds wait-timeout", 30 * time.Second, 5 * time.Minute, true, 5*time.Minute + 30*time.Second},
		{"wait-timeout 0 is no limit", 30 * time.Second, 0, true, 0},
		{"timeout 0 is no limit", 0, 5 * time.Minute, true, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := updateSetupTimeout(tc.api, tc.wait, tc.waitFlag); got != tc.want {
				t.Errorf("updateSetupTimeout = %v, want %v", got, tc.want)
			}
		})
	}
}
