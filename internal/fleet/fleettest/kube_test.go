package fleettest_test

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/dantech2000/refresh/internal/fleet/fleettest"
)

// kubeWorld returns a world whose nodegroup ng-a has two nodes with a pod
// each, and whose ng-b has one node.
func kubeWorld(script fleettest.UpdateScript) *fleettest.World {
	w := fleettest.New()
	w.Edit(func(s *fleettest.State) {
		s.DefaultUpdate = script
		s.Clusters = []*fleettest.Cluster{{
			Name: "prod", Version: "1.32",
			Nodegroups: []*fleettest.Nodegroup{{Name: "ng-a", Version: "1.32"}, {Name: "ng-b", Version: "1.32"}},
			Nodes: []*fleettest.Node{
				{Name: "a-1", Nodegroup: "ng-a"}, {Name: "a-2", Nodegroup: "ng-a"}, {Name: "b-1", Nodegroup: "ng-b"},
			},
			Pods: []*fleettest.Pod{
				{Namespace: "default", Name: "web-1", Node: "a-1"},
				{Namespace: "default", Name: "web-2", Node: "a-2"},
				{Namespace: "default", Name: "api-1", Node: "b-1"},
			},
		}}
	})
	return w
}

// kubeState describes the nodes and pods of prod, sorted.
func kubeState(w *fleettest.World) string {
	var parts []string
	w.View(func(s *fleettest.State) {
		c := s.Cluster(region, "prod")
		for _, n := range c.Nodes {
			text := n.Name
			if n.NotReady {
				text += "(NotReady)"
			}
			if n.Unschedulable {
				text += "(cordoned)"
			}
			parts = append(parts, text)
		}
		for _, p := range c.Pods {
			parts = append(parts, fmt.Sprintf("%s=%s@%s", p.Name, p.Phase, p.Node))
		}
	})
	slices.Sort(parts)
	return strings.Join(parts, " ")
}

func TestRollMovesNodesAndPods(t *testing.T) {
	const (
		before  = "a-1 a-2 api-1=Running@b-1 b-1 web-1=Running@a-1 web-2=Running@a-2"
		joined  = "a-1 a-2 api-1=Running@b-1 b-1 ng-a-u1-1(NotReady) ng-a-u1-2(NotReady) web-1=Running@a-1 web-2=Running@a-2"
		ready   = "a-1(cordoned) a-2(cordoned) api-1=Running@b-1 b-1 ng-a-u1-1 ng-a-u1-2 web-1=Running@a-1 web-2=Running@a-2"
		drained = "a-1(cordoned) a-2(cordoned) api-1=Running@b-1 b-1 ng-a-u1-1 ng-a-u1-2 web-1=Running@ng-a-u1-1 web-2=Running@ng-a-u1-2"
		done    = "api-1=Running@b-1 b-1 ng-a-u1-1 ng-a-u1-2 web-1=Running@ng-a-u1-1 web-2=Running@ng-a-u1-2"
	)
	tests := []struct {
		name string
		roll fleettest.RollScript
		// want is the state at the start of the update and after each of
		// four quarters of its duration.
		want [5]string
	}{
		{"a clean roll", fleettest.RollScript{}, [5]string{joined, ready, drained, done, done}},
		{"a drain that is stuck", fleettest.RollScript{StuckDrain: true}, [5]string{joined, ready, ready, ready, ready}},
		{
			"new nodes that never become Ready", fleettest.RollScript{NodesNeverReady: true},
			[5]string{
				joined,
				strings.NewReplacer("ng-a-u1-1", "ng-a-u1-1(NotReady)", "ng-a-u1-2", "ng-a-u1-2(NotReady)").Replace(ready),
				"a-1(cordoned) a-2(cordoned) api-1=Running@b-1 b-1 ng-a-u1-1(NotReady) ng-a-u1-2(NotReady) web-1=Running@ng-a-u1-1 web-2=Running@ng-a-u1-2",
				"api-1=Running@b-1 b-1 ng-a-u1-1(NotReady) ng-a-u1-2(NotReady) web-1=Running@ng-a-u1-1 web-2=Running@ng-a-u1-2",
				"api-1=Running@b-1 b-1 ng-a-u1-1(NotReady) ng-a-u1-2(NotReady) web-1=Running@ng-a-u1-1 web-2=Running@ng-a-u1-2",
			},
		},
		{
			"pods that stay Pending", fleettest.RollScript{PendingPods: true},
			[5]string{
				joined, ready,
				"a-1(cordoned) a-2(cordoned) api-1=Running@b-1 b-1 ng-a-u1-1 ng-a-u1-2 web-1=Pending@ web-2=Pending@",
				"api-1=Running@b-1 b-1 ng-a-u1-1 ng-a-u1-2 web-1=Pending@ web-2=Pending@",
				"api-1=Running@b-1 b-1 ng-a-u1-1 ng-a-u1-2 web-1=Pending@ web-2=Pending@",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := kubeWorld(fleettest.UpdateScript{After: 4 * time.Minute, Roll: tt.roll})
			if got := kubeState(w); got != before {
				t.Fatalf("before the update: %s", got)
			}
			generation := w.Generation()
			if _, err := w.UpdateNodegroupVersion(region, "prod", "ng-a", fleettest.NodegroupVersionRequest{}); err != nil {
				t.Fatal(err)
			}
			if w.Generation() == generation {
				t.Error("the generation did not grow when the update started")
			}
			var got [5]string
			for i := range got {
				if i > 0 {
					w.Advance(time.Minute)
				}
				got[i] = kubeState(w)
			}
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("nodes and pods by quarter (-want +got):\n%s", diff)
			}
		})
	}
}

func TestRollWithNoDurationOrNoNodes(t *testing.T) {
	w := kubeWorld(fleettest.UpdateScript{})
	if _, err := w.UpdateNodegroupVersion(region, "prod", "ng-b", fleettest.NodegroupVersionRequest{}); err != nil {
		t.Fatal(err)
	}
	if got, want := kubeState(w), "a-1 a-2 api-1=Running@ng-b-u1-1 ng-b-u1-1 web-1=Running@a-1 web-2=Running@a-2"; got != want {
		t.Errorf("after an instant roll of ng-b: %s, want %s", got, want)
	}
	// A config update is not a roll.
	three := int32(3)
	if _, err := w.UpdateNodegroupConfig(region, "prod", "ng-a", fleettest.NodegroupConfigRequest{Scaling: &fleettest.ScalingRequest{Desired: &three}}); err != nil {
		t.Fatal(err)
	}
	if got := kubeState(w); !strings.Contains(got, "a-1 a-2 ") {
		t.Errorf("a config update moved the nodes: %s", got)
	}
}

// TestChangesWakesOnEveryChange checks the handshake a streaming server
// uses: the channel taken before a change is closed by it.
func TestChangesWakesOnEveryChange(t *testing.T) {
	w := kubeWorld(fleettest.UpdateScript{})
	for name, change := range map[string]func(){
		"Edit":    func() { w.Edit(func(*fleettest.State) {}) },
		"Advance": func() { w.Advance(time.Second) },
		"update": func() {
			_, _ = w.UpdateNodegroupVersion(region, "prod", "ng-a", fleettest.NodegroupVersionRequest{})
		},
	} {
		ch, generation := w.Changes(), w.Generation()
		change()
		select {
		case <-ch:
		default:
			t.Errorf("%s did not close the channel of Changes", name)
		}
		if w.Generation() <= generation {
			t.Errorf("%s did not grow the generation", name)
		}
	}
}
