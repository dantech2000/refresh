package tui

import (
	"strings"
	"testing"

	"github.com/dantech2000/refresh/internal/tui/state"
)

// Found on a real cluster: the hints were picked by phase position, which
// the upgrade's order (control plane, nodegroups, then add-ons) broke, so
// "Nodegroups" read "update stale add-ons".
func TestPendingHintFollowsThePhaseName(t *testing.T) {
	for name, want := range map[string]string{
		"Plan":                  "readiness checks",
		"Control plane 1.35":    "UpdateClusterVersion",
		"Required add-ons 1.35": "add-ons the new control plane needs first",
		"Nodegroups 1.35":       "roll each nodegroup",
		"Add-ons 1.35":          "update stale add-ons",
		"Pre-flight":            "readiness checks",
		"Verify":                "nodes, pods, add-on health",
		"Something else":        "",
	} {
		if got := pendingHint(name); got != want {
			t.Errorf("pendingHint(%q) = %q, want %q", name, got, want)
		}
	}
}

// Found on a real cluster: a pod name longer than the name column ran into
// its reason ("coredns-6988fc6dc6-5nhjkwaiting").
func TestPodCardKeepsAGapAfterLongNames(t *testing.T) {
	long := "kube-system/coredns-6988fc6dc6-5nhjk"
	r := state.Roll{Pods: map[string][]state.Pod{"n1": {
		{Name: "default/web-6bbb4849d5-gj2jg"},
		{Name: long},
	}}}
	for _, w := range []int{70, 40} {
		for _, l := range (Model{}).podCard(r, "n1", w) {
			var b strings.Builder
			for _, s := range l {
				b.WriteString(s.Text)
			}
			line := b.String()
			if strings.Contains(line, "waiting") && !strings.Contains(line, "  waiting") {
				t.Errorf("width %d: no gap before the reason: %q", w, line)
			}
			if width(line) > w {
				t.Errorf("width %d: line is %d cells: %q", w, width(line), line)
			}
		}
	}
}
