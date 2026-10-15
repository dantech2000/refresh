package noderoll

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"
)

// The pressure conditions of a node are reported in a fixed order, a
// condition that is false is left out, and pressure never changes the
// phase of a Ready node.
func TestClassify_PressureAdvisory(t *testing.T) {
	n := mkNode("ip-hot", newAMI, true, false)
	// Out of order, and one condition that is false.
	n.Status.Conditions = append(n.Status.Conditions,
		corev1.NodeCondition{Type: corev1.NodeDiskPressure, Status: corev1.ConditionTrue},
		corev1.NodeCondition{Type: corev1.NodePIDPressure, Status: corev1.ConditionFalse},
		corev1.NodeCondition{Type: corev1.NodeMemoryPressure, Status: corev1.ConditionTrue},
	)
	v := classify(n, newAMI, nil)
	if v.Phase != PhaseReady || !v.Ready {
		t.Fatalf("pressure changed the phase: phase %s, ready %v", v.Phase, v.Ready)
	}
	if diff := cmp.Diff([]string{"MemoryPressure", "DiskPressure"}, v.Pressure); diff != "" {
		t.Errorf("pressure (-want +got):\n%s", diff)
	}
	if got := classify(mkNode("ip-cool", newAMI, true, false), newAMI, nil).Pressure; got != nil {
		t.Errorf("a node under no pressure reports %v", got)
	}
}

// The classifier table: the phase from the cordon, the drain taints, and
// readiness, and old or new from the baseline, or from the image label
// when there is no baseline.
func TestClassify(t *testing.T) {
	tainted := func(key string) *corev1.Node {
		n := mkNode("ip-1", oldAMI, true, false)
		n.Spec.Taints = []corev1.Taint{{Key: key, Effect: corev1.TaintEffectNoSchedule}}
		return n
	}
	noCondition := mkNode("ip-1", oldAMI, true, false)
	noCondition.Status.Conditions = nil
	tests := []struct {
		name      string
		node      *corev1.Node
		baseline  map[string]bool
		wantPhase Phase
		wantNew   bool
	}{
		{name: "Ready", node: mkNode("ip-1", oldAMI, true, false), wantPhase: PhaseReady},
		{name: "not Ready", node: mkNode("ip-1", oldAMI, false, false), wantPhase: PhaseJoining},
		{name: "no Ready condition", node: noCondition, wantPhase: PhaseJoining},
		{name: "cordoned", node: mkNode("ip-1", oldAMI, true, true), wantPhase: PhaseDraining},
		{name: "cordoned and not Ready", node: mkNode("ip-1", oldAMI, false, true), wantPhase: PhaseDraining},
		{name: "the taint of a cordon", node: tainted("node.kubernetes.io/unschedulable"), wantPhase: PhaseDraining},
		{name: "to be deleted by the autoscaler", node: tainted("ToBeDeletedByClusterAutoscaler"), wantPhase: PhaseDraining},
		{name: "a deletion candidate of the autoscaler", node: tainted("DeletionCandidateOfClusterAutoscaler"), wantPhase: PhaseDraining},
		{name: "another taint", node: tainted("dedicated"), wantPhase: PhaseReady},
		{name: "the image label: the target AMI", node: mkNode("ip-1", newAMI, true, false), wantPhase: PhaseReady, wantNew: true},
		{name: "the baseline: an old node", node: mkNode("ip-1", newAMI, true, false), baseline: map[string]bool{"ip-1": true}, wantPhase: PhaseReady},
		{name: "the baseline: a node that came later", node: mkNode("ip-1", oldAMI, true, false), baseline: map[string]bool{"ip-0": true}, wantPhase: PhaseReady, wantNew: true},
		{name: "an empty baseline: every node is new", node: mkNode("ip-1", oldAMI, true, false), baseline: map[string]bool{}, wantPhase: PhaseReady, wantNew: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v := classify(tt.node, newAMI, tt.baseline)
			if v.Phase != tt.wantPhase || v.OnTarget != tt.wantNew {
				t.Errorf("classify = phase %s, new %v; want %s, %v", v.Phase, v.OnTarget, tt.wantPhase, tt.wantNew)
			}
		})
	}
}

func TestKubeletMinor(t *testing.T) {
	for in, want := range map[string]string{"v1.35.3-eks-7a2b9c0": "1.35", "1.34.6": "1.34", "": "", "v1": "", "garbage": "", "v1.": ""} {
		n := &corev1.Node{}
		n.Status.NodeInfo.KubeletVersion = in
		if got := kubeletMinor(n); got != want {
			t.Errorf("kubeletMinor(%q) = %q, want %q", in, got, want)
		}
	}
}
