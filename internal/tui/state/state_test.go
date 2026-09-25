package state

import (
	"testing"

	"github.com/dantech2000/refresh/internal/noderoll"
)

func TestVersions(t *testing.T) {
	cases := []struct {
		v     string
		minor int
		next  string
	}{
		{"1.31", 31, "1.32"},
		{"1.9", 9, "1.10"},
		{"1.33.4", 33, "1.34"},
		{"x", -1, ""},
		{"1.x", -1, ""},
	}
	for _, c := range cases {
		if got := Minor(c.v); got != c.minor {
			t.Errorf("Minor(%q) = %d, want %d", c.v, got, c.minor)
		}
		if got := NextMinor(c.v); got != c.next {
			t.Errorf("NextMinor(%q) = %q, want %q", c.v, got, c.next)
		}
	}
}

func TestClusterHealth(t *testing.T) {
	base := Cluster{Version: "1.33", Latest: "1.33"}
	cases := []struct {
		name string
		c    func(Cluster) Cluster
		lvl  Level
		text string
	}{
		{"current", func(c Cluster) Cluster { return c }, LevelOK, "current"},
		{"busy wins", func(c Cluster) Cluster { c.Busy = "upgrading"; c.ExtendedSupport = true; return c }, LevelProgress, "upgrading"},
		{"extended", func(c Cluster) Cluster { c.ExtendedSupport = true; return c }, LevelError, "extended support"},
		{"one behind", func(c Cluster) Cluster { c.Version = "1.32"; return c }, LevelWarn, "1 minor behind"},
		{"two behind", func(c Cluster) Cluster { c.Version = "1.31"; return c }, LevelWarn, "2 minors behind"},
		{"stale AMI", func(c Cluster) Cluster {
			c.Nodegroups = []Nodegroup{{Version: "1.33", AMI: "a", LatestAMI: "b"}}
			return c
		}, LevelWarn, "AMI patches"},
		{"stale add-on", func(c Cluster) Cluster {
			c.Addons = []Addon{{Version: "v1", Latest: "v2"}}
			return c
		}, LevelWarn, "add-on updates"},
	}
	for _, tc := range cases {
		lvl, text := tc.c(base).Health()
		if lvl != tc.lvl || text != tc.text {
			t.Errorf("%s: Health = %d %q, want %d %q", tc.name, lvl, text, tc.lvl, tc.text)
		}
	}
}

func TestRollReplacedAndUpgradeProgress(t *testing.T) {
	r := Roll{Planned: 3, Snapshot: noderoll.Snapshot{Nodes: []noderoll.NodeView{
		{Name: "a", OnTarget: false}, {Name: "b", OnTarget: true}, {Name: "c", OnTarget: true},
	}}}
	if got := r.Replaced(); got != 2 {
		t.Fatalf("Replaced = %d, want 2", got)
	}
	u := Upgrade{Phases: []Phase{
		{Status: PhaseDone, Weight: 0.5},
		{Status: PhaseRunning, Weight: 0.5, Progress: 0.5},
	}}
	if got := u.Progress(); got != 0.75 {
		t.Fatalf("Progress = %v, want 0.75", got)
	}
	if got := u.Current(); got != 1 {
		t.Fatalf("Current = %d, want 1", got)
	}
	if (Upgrade{}).Progress() != 0 || (Upgrade{}).Current() != -1 {
		t.Fatal("empty upgrade")
	}
}
