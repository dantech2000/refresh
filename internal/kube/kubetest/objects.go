package kubetest

import (
	"fmt"
	"maps"
	"slices"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	metricsv1beta1 "k8s.io/metrics/pkg/apis/metrics/v1beta1"

	"github.com/dantech2000/refresh/internal/fleet/fleettest"
)

// The node labels that EKS sets and refresh reads.
const (
	labelNodegroup = "eks.amazonaws.com/nodegroup"
	labelImage     = "eks.amazonaws.com/nodegroup-image"
)

func meta(namespace, name string, labels map[string]string, created time.Time) metav1.ObjectMeta {
	return metav1.ObjectMeta{
		Namespace: namespace, Name: name, Labels: labels,
		UID:               types.UID("uid-" + namespace + "-" + name),
		CreationTimestamp: metav1.NewTime(created.UTC()),
	}
}

func namespaces(c *fleettest.Cluster) []item {
	names := []string{"default", "kube-node-lease", "kube-public", "kube-system"}
	add := func(ns string) {
		if ns != "" && !slices.Contains(names, ns) {
			names = append(names, ns)
		}
	}
	for _, p := range c.Pods {
		add(p.Namespace)
	}
	for _, p := range c.PDBs {
		add(p.Namespace)
	}
	for _, d := range c.Deployments {
		add(d.Namespace)
	}
	slices.Sort(names)
	out := make([]item, 0, len(names))
	for _, name := range names {
		out = append(out, item{
			name: name, fields: map[string]string{"metadata.name": name}, summary: name,
			object: corev1.Namespace{
				TypeMeta: metav1.TypeMeta{Kind: "Namespace", APIVersion: "v1"}, ObjectMeta: meta("", name, nil, c.CreatedAt),
				Status: corev1.NamespaceStatus{Phase: corev1.NamespaceActive},
			},
		})
	}
	return out
}

func nodes(c *fleettest.Cluster) []item {
	out := make([]item, 0, len(c.Nodes))
	for _, n := range c.Nodes {
		labels := map[string]string{"kubernetes.io/hostname": n.Name}
		maps.Copy(labels, n.Labels)
		if n.Nodegroup != "" {
			labels[labelNodegroup] = n.Nodegroup
		}
		if n.ImageID != "" {
			labels[labelImage] = n.ImageID
		}
		ready, reason, summary := corev1.ConditionTrue, "KubeletReady", n.Name+"=Ready"
		if n.NotReady {
			ready, reason, summary = corev1.ConditionFalse, "KubeletNotReady", n.Name+"=NotReady"
		}
		if n.ReadyReason != "" {
			reason = n.ReadyReason
		}
		node := corev1.Node{
			TypeMeta: metav1.TypeMeta{Kind: "Node", APIVersion: "v1"}, ObjectMeta: meta("", n.Name, labels, n.CreatedAt),
			Spec: corev1.NodeSpec{Unschedulable: n.Unschedulable, ProviderID: "aws:///us-east-1a/" + n.Name},
			Status: corev1.NodeStatus{
				Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: ready, Reason: reason}},
				Capacity:   capacity(n.MilliCPU, n.MemoryMi), Allocatable: capacity(n.MilliCPU, n.MemoryMi),
			},
		}
		if n.Unschedulable {
			node.Spec.Taints = []corev1.Taint{{Key: corev1.TaintNodeUnschedulable, Effect: corev1.TaintEffectNoSchedule}}
			summary += ",cordoned"
		}
		out = append(out, item{
			name: n.Name, labels: labels, fields: map[string]string{"metadata.name": n.Name, "spec.unschedulable": fmt.Sprint(n.Unschedulable)},
			object: node, summary: summary,
		})
	}
	return out
}

func capacity(milliCPU, memoryMi int64) corev1.ResourceList {
	return corev1.ResourceList{
		corev1.ResourceCPU:    *resource.NewMilliQuantity(milliCPU, resource.DecimalSI),
		corev1.ResourceMemory: *resource.NewQuantity(memoryMi<<20, resource.BinarySI),
		corev1.ResourcePods:   *resource.NewQuantity(110, resource.DecimalSI),
	}
}

func pods(c *fleettest.Cluster) []item {
	out := make([]item, 0, len(c.Pods))
	for _, p := range c.Pods {
		phase := corev1.PodPhase(p.Phase)
		running := phase == corev1.PodRunning
		ready := running && !p.NotReady
		pod := corev1.Pod{
			TypeMeta: metav1.TypeMeta{Kind: "Pod", APIVersion: "v1"}, ObjectMeta: meta(p.Namespace, p.Name, p.Labels, p.CreatedAt),
			Spec: corev1.PodSpec{NodeName: p.Node, Containers: []corev1.Container{{Name: "main", Image: "fake"}}},
			Status: corev1.PodStatus{
				Phase: phase, Reason: p.Reason,
				Conditions: []corev1.PodCondition{
					{Type: corev1.PodScheduled, Status: condition(p.Node != "")},
					{Type: corev1.PodReady, Status: condition(ready)},
				},
				ContainerStatuses: []corev1.ContainerStatus{{Name: "main", Ready: ready, Started: &running}},
			},
		}
		if p.DisruptionTarget {
			pod.Status.Conditions = append(pod.Status.Conditions, corev1.PodCondition{
				Type: corev1.DisruptionTarget, Status: corev1.ConditionTrue, Reason: corev1.PodReasonTerminationByKubelet,
			})
		}
		if p.Deleting {
			now := metav1.NewTime(p.CreatedAt.UTC())
			pod.DeletionTimestamp = &now
		}
		summary := p.Namespace + "/" + p.Name + "=" + p.Phase
		if running && p.NotReady {
			summary += ",notReady"
		}
		if p.Node != "" {
			summary += "@" + p.Node
		}
		out = append(out, item{
			namespace: p.Namespace, name: p.Name, labels: p.Labels,
			fields: map[string]string{"metadata.name": p.Name, "metadata.namespace": p.Namespace, "spec.nodeName": p.Node, "status.phase": p.Phase},
			object: pod, summary: summary,
		})
	}
	return out
}

func condition(ok bool) corev1.ConditionStatus {
	if ok {
		return corev1.ConditionTrue
	}
	return corev1.ConditionFalse
}

func pdbs(c *fleettest.Cluster) []item {
	out := make([]item, 0, len(c.PDBs))
	for _, p := range c.PDBs {
		minAvailable := intstr.FromInt32(p.MinAvailable)
		budget := policyv1.PodDisruptionBudget{
			TypeMeta:   metav1.TypeMeta{Kind: "PodDisruptionBudget", APIVersion: "policy/v1"},
			ObjectMeta: withGeneration(meta(p.Namespace, p.Name, nil, c.CreatedAt)),
			Spec:       policyv1.PodDisruptionBudgetSpec{MinAvailable: &minAvailable, Selector: &metav1.LabelSelector{MatchLabels: p.Selector}},
			Status: policyv1.PodDisruptionBudgetStatus{
				ObservedGeneration: 1, DisruptionsAllowed: p.DisruptionsAllowed,
				CurrentHealthy: p.CurrentHealthy, DesiredHealthy: p.DesiredHealthy, ExpectedPods: p.ExpectedPods,
				Conditions: []metav1.Condition{{Type: policyv1.DisruptionAllowedCondition, Status: metav1.ConditionTrue, Reason: policyv1.SufficientPodsReason}},
			},
		}
		if p.NoSelector {
			budget.Spec.Selector = nil
		}
		if p.MaxUnavailable != nil {
			maxUnavailable := intstr.FromInt32(*p.MaxUnavailable)
			budget.Spec.MinAvailable, budget.Spec.MaxUnavailable = nil, &maxUnavailable
		}
		if p.AlwaysAllow {
			policy := policyv1.AlwaysAllow
			budget.Spec.UnhealthyPodEvictionPolicy = &policy
		}
		if p.Unsynced {
			budget.Status.ObservedGeneration = 0
		}
		if p.SyncFailed {
			budget.Status.Conditions[0] = metav1.Condition{Type: policyv1.DisruptionAllowedCondition, Status: metav1.ConditionFalse, Reason: policyv1.SyncFailedReason}
		}
		out = append(out, item{
			namespace: p.Namespace, name: p.Name, fields: map[string]string{"metadata.name": p.Name, "metadata.namespace": p.Namespace},
			summary: fmt.Sprintf("%s/%s=allows%d", p.Namespace, p.Name, p.DisruptionsAllowed),
			object:  budget,
		})
	}
	return out
}

func withGeneration(m metav1.ObjectMeta) metav1.ObjectMeta {
	m.Generation = 1
	return m
}

func deployments(c *fleettest.Cluster) []item {
	out := make([]item, 0, len(c.Deployments))
	for _, d := range c.Deployments {
		replicas := d.Replicas
		out = append(out, item{
			namespace: d.Namespace, name: d.Name, labels: d.Selector,
			fields:  map[string]string{"metadata.name": d.Name, "metadata.namespace": d.Namespace},
			summary: fmt.Sprintf("%s/%s=%d", d.Namespace, d.Name, d.Replicas),
			object: appsv1.Deployment{
				TypeMeta: metav1.TypeMeta{Kind: "Deployment", APIVersion: "apps/v1"}, ObjectMeta: meta(d.Namespace, d.Name, d.Selector, c.CreatedAt),
				Spec: appsv1.DeploymentSpec{
					Replicas: &replicas, Selector: &metav1.LabelSelector{MatchLabels: d.Selector},
					Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: d.Selector}},
				},
				Status: appsv1.DeploymentStatus{Replicas: d.Replicas, ReadyReplicas: d.Replicas, AvailableReplicas: d.Replicas},
			},
		})
	}
	return out
}

func events(c *fleettest.Cluster) []item {
	out := make([]item, 0, len(c.Events))
	for _, e := range c.Events {
		at := metav1.NewTime(e.At.UTC())
		out = append(out, item{
			namespace: e.Namespace, name: e.Name,
			fields: map[string]string{
				"metadata.name": e.Name, "metadata.namespace": e.Namespace, "type": e.Type, "reason": e.Reason,
				"involvedObject.kind": e.Kind, "involvedObject.name": e.Object,
			},
			summary: fmt.Sprintf("%s/%s=%s:%s", e.Kind, e.Object, e.Type, e.Reason),
			object: corev1.Event{
				TypeMeta: metav1.TypeMeta{Kind: "Event", APIVersion: "v1"}, ObjectMeta: meta(e.Namespace, e.Name, nil, e.At),
				InvolvedObject: corev1.ObjectReference{Kind: e.Kind, Name: e.Object, Namespace: e.Namespace},
				Type:           e.Type, Reason: e.Reason, Message: e.Message, FirstTimestamp: at, LastTimestamp: at, Count: 1,
			},
		})
	}
	return out
}

func nodeMetrics(c *fleettest.Cluster) []item {
	out := make([]item, 0, len(c.Nodes))
	for _, n := range c.Nodes {
		out = append(out, item{
			name: n.Name, labels: nodegroupLabel(n), fields: map[string]string{"metadata.name": n.Name},
			summary: fmt.Sprintf("%s=%dm", n.Name, n.UsedMilliCPU),
			object: metricsv1beta1.NodeMetrics{
				TypeMeta: metav1.TypeMeta{Kind: "NodeMetrics", APIVersion: "metrics.k8s.io/v1beta1"}, ObjectMeta: meta("", n.Name, nil, n.CreatedAt),
				Window: metav1.Duration{Duration: 30 * time.Second},
				Usage: corev1.ResourceList{
					corev1.ResourceCPU:    *resource.NewMilliQuantity(n.UsedMilliCPU, resource.DecimalSI),
					corev1.ResourceMemory: *resource.NewQuantity(n.UsedMemoryMi<<20, resource.BinarySI),
				},
			},
		})
	}
	return out
}

// nodegroupLabel is the nodegroup label of a node, or none for a node of
// no managed nodegroup.
func nodegroupLabel(n *fleettest.Node) map[string]string {
	if n.Nodegroup == "" {
		return map[string]string{}
	}
	return map[string]string{labelNodegroup: n.Nodegroup}
}
