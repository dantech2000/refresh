package noderoll

import (
	"strings"

	corev1 "k8s.io/api/core/v1"
)

// classify returns the view of one node. A node that is cordoned or has a
// drain taint is Draining, one that is not Ready is Joining, and any other
// is Ready.
//
// baseline holds the old nodes of the roll, by identity (see nodeKey): a
// node that is not in it is new. With a nil baseline the image label
// decides: a node is new when it was launched with targetAMI. An Observer always has a
// baseline; the label rule is kept for a caller that knows the AMI.
func classify(n *corev1.Node, targetAMI string, baseline map[string]bool) NodeView {
	ready := nodeReady(n)
	phase := PhaseReady
	switch {
	case n.Spec.Unschedulable || hasDrainTaint(n):
		phase = PhaseDraining
	case !ready:
		phase = PhaseJoining
	}
	onTarget := n.Labels[LabelImage] == targetAMI
	if baseline != nil {
		onTarget = !baseline[nodeKey(n)]
	}
	return NodeView{
		key:      nodeKey(n),
		Name:     n.Name,
		OnTarget: onTarget,
		Ready:    ready,
		Phase:    phase,
		Pressure: pressureConditions(n),
	}
}

// nodeKey is the identity of a node: its UID, which the API server gives
// each node object once. EKS names a node after its private IP, and EC2
// can give the IP of a terminated node to a new one, so a name can come
// back. A node without a UID, as some fakes make it, is known by its name.
func nodeKey(n *corev1.Node) string {
	if n.UID != "" {
		return "uid:" + string(n.UID)
	}
	return "name:" + n.Name
}

// kubeletMinor returns the major and minor version of the kubelet of a
// node, such as "1.35" for "v1.35.2-eks-1", or "" when it cannot be read.
func kubeletMinor(n *corev1.Node) string {
	v := strings.TrimPrefix(n.Status.NodeInfo.KubeletVersion, "v")
	major, rest, ok := strings.Cut(v, ".")
	if !ok || major == "" {
		return ""
	}
	minor, _, _ := strings.Cut(rest, ".")
	if minor == "" {
		return ""
	}
	return major + "." + minor
}

// pressureConditions returns the names of the pressure conditions of a
// node that are true, in a fixed order: memory, disk, PID, network. It
// returns nil for a node under no pressure.
func pressureConditions(n *corev1.Node) []string {
	active := make(map[corev1.NodeConditionType]bool, len(n.Status.Conditions))
	for _, c := range n.Status.Conditions {
		if c.Status == corev1.ConditionTrue {
			active[c.Type] = true
		}
	}
	var out []string
	for _, t := range []corev1.NodeConditionType{
		corev1.NodeMemoryPressure, corev1.NodeDiskPressure, corev1.NodePIDPressure, corev1.NodeNetworkUnavailable,
	} {
		if active[t] {
			out = append(out, string(t))
		}
	}
	return out
}

// nodeReady reports whether the Ready condition of a node is true. A node
// with no Ready condition is not Ready.
func nodeReady(n *corev1.Node) bool {
	for _, c := range n.Status.Conditions {
		if c.Type == corev1.NodeReady {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}

// hasDrainTaint reports whether a node has a taint that says it is being
// removed: the one of a cordon, or the one the cluster autoscaler sets
// before it drains a node. DeletionCandidateOfClusterAutoscaler does not
// count: it is a soft PreferNoSchedule taint on a node that the autoscaler
// may remove later, and it takes the taint off again.
func hasDrainTaint(n *corev1.Node) bool {
	for _, t := range n.Spec.Taints {
		if strings.HasPrefix(t.Key, "ToBeDeletedByClusterAutoscaler") ||
			t.Key == corev1.TaintNodeUnschedulable {
			return true
		}
	}
	return false
}
