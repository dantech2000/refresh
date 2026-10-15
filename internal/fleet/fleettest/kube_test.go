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
		drained = "a-1(cordoned) a-2(cordoned) api-1=Running@b-1 b-1 ng-a-u1-1 ng-a-u1-2 web-1-u1=Running@ng-a-u1-1 web-2-u1=Running@ng-a-u1-2"
		done    = "api-1=Running@b-1 b-1 ng-a-u1-1 ng-a-u1-2 web-1-u1=Running@ng-a-u1-1 web-2-u1=Running@ng-a-u1-2"
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
				"a-1(cordoned) a-2(cordoned) api-1=Running@b-1 b-1 ng-a-u1-1(NotReady) ng-a-u1-2(NotReady) web-1-u1=Running@ng-a-u1-1 web-2-u1=Running@ng-a-u1-2",
				"api-1=Running@b-1 b-1 ng-a-u1-1(NotReady) ng-a-u1-2(NotReady) web-1-u1=Running@ng-a-u1-1 web-2-u1=Running@ng-a-u1-2",
				"api-1=Running@b-1 b-1 ng-a-u1-1(NotReady) ng-a-u1-2(NotReady) web-1-u1=Running@ng-a-u1-1 web-2-u1=Running@ng-a-u1-2",
			},
		},
		{
			"pods that stay Pending", fleettest.RollScript{PendingPods: true},
			[5]string{
				joined, ready,
				"a-1(cordoned) a-2(cordoned) api-1=Running@b-1 b-1 ng-a-u1-1 ng-a-u1-2 web-1-u1=Pending@ web-2-u1=Pending@",
				"api-1=Running@b-1 b-1 ng-a-u1-1 ng-a-u1-2 web-1-u1=Pending@ web-2-u1=Pending@",
				"api-1=Running@b-1 b-1 ng-a-u1-1 ng-a-u1-2 web-1-u1=Pending@ web-2-u1=Pending@",
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
	if got, want := kubeState(w), "a-1 a-2 api-1-u1=Running@ng-b-u1-1 ng-b-u1-1 web-1=Running@a-1 web-2=Running@a-2"; got != want {
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

// TestRollReplacesInstances checks the EC2 side of a roll: the instances of
// the nodegroup's Auto Scaling group are replaced by new ones on the image
// SSM recommends, which the new nodes carry too, and each launch and
// termination is a scaling activity.
func TestRollReplacesInstances(t *testing.T) {
	w := kubeWorld(fleettest.UpdateScript{After: 4 * time.Minute})
	w.Edit(func(s *fleettest.State) {
		c := s.Cluster(region, "prod")
		c.Nodegroup("ng-a").AutoScalingGroups = []string{"eks-ng-a"}
		s.AutoScalingGroups = []fleettest.AutoScalingGroup{{Name: "eks-ng-a", InstanceIDs: []string{"i-old-1", "i-old-2"}}, {Name: "other", InstanceIDs: []string{"i-x"}}}
		s.Instances = []fleettest.Instance{
			{ID: "i-old-1", ImageID: "ami-old", Type: "m5.large", AvailabilityZone: "us-east-1a", Tags: map[string]string{"k": "v"}},
			{ID: "i-old-2", ImageID: "ami-old", Type: "m5.large"}, {ID: "i-x", ImageID: "ami-x"},
		}
		s.Metrics = []fleettest.Metric{{Namespace: "AWS/EC2", Name: "CPUUtilization", Dimensions: map[string]string{"InstanceId": "i-old-1"}, Values: []float64{40}}}
	})
	ec2 := func() string {
		var parts []string
		w.View(func(s *fleettest.State) {
			parts = append(parts, "group="+strings.Join(s.AutoScalingGroups[0].InstanceIDs, ","))
			for _, in := range s.Instances {
				parts = append(parts, in.ID+":"+in.ImageID+":"+in.Type)
			}
			for _, m := range s.Metrics {
				parts = append(parts, "cpu:"+m.Dimensions["InstanceId"])
			}
			for _, a := range s.ScalingActivities {
				parts = append(parts, a.Description)
			}
		})
		return strings.Join(parts, " | ")
	}

	if _, err := w.UpdateNodegroupVersion(region, "prod", "ng-a", fleettest.NodegroupVersionRequest{Version: new("1.33")}); err != nil {
		t.Fatal(err)
	}
	const during = "group=i-old-1,i-old-2,i-ng-a-u1-1,i-ng-a-u1-2 | i-old-1:ami-old:m5.large | i-old-2:ami-old:m5.large | i-x:ami-x: | " +
		"i-ng-a-u1-1:ami-latest133:m5.large | i-ng-a-u1-2:ami-latest133:m5.large | cpu:i-old-1 | cpu:i-ng-a-u1-1 | " +
		"Launching a new EC2 instance: i-ng-a-u1-2 | Launching a new EC2 instance: i-ng-a-u1-1"
	if got := ec2(); got != during {
		t.Errorf("at the start of the roll:\n got %s\nwant %s", got, during)
	}
	w.Advance(4 * time.Minute)
	const after = "group=i-ng-a-u1-1,i-ng-a-u1-2 | i-x:ami-x: | i-ng-a-u1-1:ami-latest133:m5.large | i-ng-a-u1-2:ami-latest133:m5.large | cpu:i-ng-a-u1-1 | " +
		"Terminating EC2 instance: i-old-2 | Terminating EC2 instance: i-old-1 | " +
		"Launching a new EC2 instance: i-ng-a-u1-2 | Launching a new EC2 instance: i-ng-a-u1-1"
	if got := ec2(); got != after {
		t.Errorf("after the roll:\n got %s\nwant %s", got, after)
	}
	w.View(func(s *fleettest.State) {
		image, _ := s.Parameter(region, "/aws/service/eks/optimized-ami/1.33/amazon-linux-2023/x86_64/standard/recommended/image_id")
		for _, n := range s.Cluster(region, "prod").Nodes {
			if n.Nodegroup == "ng-a" && n.ImageID != image {
				t.Errorf("node %s runs %s, and SSM recommends %s", n.Name, n.ImageID, image)
			}
		}
	})
}
