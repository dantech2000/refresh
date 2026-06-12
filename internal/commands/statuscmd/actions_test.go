package statuscmd

import (
	"testing"

	"github.com/urfave/cli/v3"

	statussvc "github.com/dantech2000/refresh/internal/services/status"
)

func exitCode(err error) int {
	if err == nil {
		return 0
	}
	if ec, ok := err.(cli.ExitCoder); ok {
		return ec.ExitCode()
	}
	return -1
}

func TestExitForStatuses(t *testing.T) {
	cases := []struct {
		name     string
		statuses []statussvc.ClusterStatus
		want     int
	}{
		{
			name:     "all current",
			statuses: []statussvc.ClusterStatus{{Support: statussvc.SupportPosture{Tier: statussvc.SupportStandard}}},
			want:     0,
		},
		{
			name: "stale only → 2",
			statuses: []statussvc.ClusterStatus{{
				Support:  statussvc.SupportPosture{Tier: statussvc.SupportStandard},
				StaleAMI: statussvc.StaleAMISummary{Behind: 1, Total: 3},
			}},
			want: 2,
		},
		{
			name: "extended support → 3 (beats stale)",
			statuses: []statussvc.ClusterStatus{{
				Support:  statussvc.SupportPosture{Tier: statussvc.SupportExtended},
				StaleAMI: statussvc.StaleAMISummary{Behind: 1},
			}},
			want: 3,
		},
		{
			name: "unsupported → 3",
			statuses: []statussvc.ClusterStatus{{
				Support: statussvc.SupportPosture{Tier: statussvc.SupportUnsupported},
			}},
			want: 3,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := exitCode(exitForStatuses(tc.statuses)); got != tc.want {
				t.Errorf("exit code = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestSortStatuses_ByStaleDescending(t *testing.T) {
	statuses := []statussvc.ClusterStatus{
		{Name: "a", StaleAMI: statussvc.StaleAMISummary{Behind: 0}},
		{Name: "b", StaleAMI: statussvc.StaleAMISummary{Behind: 5}},
		{Name: "c", StaleAMI: statussvc.StaleAMISummary{Behind: 2}},
	}
	sortStatuses(statuses, "stale", true)
	if statuses[0].Name != "b" || statuses[1].Name != "c" || statuses[2].Name != "a" {
		t.Errorf("stale desc order = %s,%s,%s, want b,c,a", statuses[0].Name, statuses[1].Name, statuses[2].Name)
	}
}

func TestSortStatuses_ByClusterName(t *testing.T) {
	statuses := []statussvc.ClusterStatus{{Name: "c"}, {Name: "a"}, {Name: "b"}}
	sortStatuses(statuses, "cluster", false)
	if statuses[0].Name != "a" || statuses[2].Name != "c" {
		t.Errorf("name order = %s..%s, want a..c", statuses[0].Name, statuses[2].Name)
	}
}
