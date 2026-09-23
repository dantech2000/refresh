package health

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	fakek8s "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/dantech2000/refresh/internal/mocks"
)

// ──────────────────────────────────────────────────────────────────────────────
// CheckCriticalWorkloads: pods left behind by a graceful node shutdown
// ──────────────────────────────────────────────────────────────────────────────

// shutdownPod returns a kube-system pod as a kubelet >= 1.26 leaves it after a
// graceful node shutdown: Failed, reason Terminated, the kubelet shutdown
// message, and a DisruptionTarget condition with reason TerminationByKubelet.
func shutdownPod(name string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: "kube-system", Name: name},
		Spec:       corev1.PodSpec{NodeName: "ip-10-0-1-23.ec2.internal"},
		Status: corev1.PodStatus{
			Phase:   corev1.PodFailed,
			Reason:  "Terminated",
			Message: "Pod was terminated in response to imminent node shutdown.",
			Conditions: []corev1.PodCondition{
				{Type: corev1.DisruptionTarget, Status: corev1.ConditionTrue, Reason: corev1.PodReasonTerminationByKubelet,
					Message: "Pod was terminated in response to imminent node shutdown."},
				{Type: corev1.PodReady, Status: corev1.ConditionFalse, Reason: "PodFailed"},
			},
			ContainerStatuses: []corev1.ContainerStatus{{
				Name: "coredns",
				State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{
					ExitCode: 137, Reason: "Error",
				}},
			}},
		},
	}
}

func TestCheckCriticalWorkloads_GracefulShutdownPodsSkipped(t *testing.T) {
	// kubelet 1.22-1.25 sets the message but no DisruptionTarget condition.
	messageOnly := shutdownPod("coredns-old-2")
	messageOnly.Status.Conditions = messageOnly.Status.Conditions[1:]
	// kubelet 1.21 used reason Shutdown.
	legacy := shutdownPod("coredns-old-3")
	legacy.Status.Reason = "Shutdown"
	legacy.Status.Conditions = nil

	client := fakek8s.NewSimpleClientset(
		allReadyPod("kube-system", "coredns-new-1"),
		allReadyPod("kube-system", "coredns-new-2"),
		shutdownPod("coredns-old-1"),
		messageOnly,
		legacy,
	)
	result := NewChecker(nil, client, nil, nil).CheckCriticalWorkloads(context.Background())
	if result.Status != StatusPass {
		t.Fatalf("pods left by a graceful node shutdown must be skipped: status = %s, msg = %s, details = %v",
			result.Status, result.Message, result.Details)
	}
	if result.Message != "2/2 critical pods running" {
		t.Errorf("message = %q, want 2/2 critical pods running", result.Message)
	}
}

func TestCheckCriticalWorkloads_TerminatedWithoutShutdownEvidenceCounts(t *testing.T) {
	// Reason Terminated alone is generic: without the DisruptionTarget
	// condition or the kubelet shutdown message the pod still counts.
	failed := shutdownPod("coredns-1")
	failed.Status.Message = "container exited"
	failed.Status.Conditions = []corev1.PodCondition{
		{Type: corev1.DisruptionTarget, Status: corev1.ConditionTrue, Reason: "PreemptionByScheduler"},
	}
	client := fakek8s.NewSimpleClientset(allReadyPod("kube-system", "coredns-2"), failed)
	result := NewChecker(nil, client, nil, nil).CheckCriticalWorkloads(context.Background())
	if result.Status == StatusPass || !hasDetail(result.Details, "kube-system/coredns-1 (Failed)") {
		t.Errorf("a Failed pod without shutdown evidence must count: status = %s, details = %v", result.Status, result.Details)
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// PDB drain blockers: pods covered by more than one PDB
// ──────────────────────────────────────────────────────────────────────────────

// labeledPod returns a Running, Ready pod with the given labels on node.
func labeledPod(namespace, name, node string, lbls map[string]string) *corev1.Pod {
	p := appPod(namespace, name, "", node)
	p.Labels = lbls
	return p
}

// selectorPDB returns a PDB over the given labels that allows `allowed` of
// `expected` pods to be disrupted.
func selectorPDB(namespace, name string, match map[string]string, allowed, expected int32) *policyv1.PodDisruptionBudget {
	return &policyv1.PodDisruptionBudget{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name},
		Spec:       policyv1.PodDisruptionBudgetSpec{Selector: &metav1.LabelSelector{MatchLabels: match}},
		Status: policyv1.PodDisruptionBudgetStatus{
			DisruptionsAllowed: allowed,
			CurrentHealthy:     expected,
			DesiredHealthy:     expected - allowed,
			ExpectedPods:       expected,
		},
	}
}

// overlappingPDBCluster has web pods that both web-pdb (app=web) and
// frontend-pdb (tier=frontend) select. Each PDB alone allows 1 disruption.
// web-1 runs on ng-a, web-2 on ng-b.
func overlappingPDBCluster(extra ...runtime.Object) *fakek8s.Clientset {
	web := map[string]string{"app": "web", "tier": "frontend"}
	objs := []runtime.Object{
		userNamespace("my-app"),
		ngNode("node-a1", "ng-a"),
		ngNode("node-b1", "ng-b"),
		labeledPod("my-app", "web-1", "node-a1", web),
		labeledPod("my-app", "web-2", "node-b1", web),
		selectorPDB("my-app", "web-pdb", map[string]string{"app": "web"}, 1, 2),
		selectorPDB("my-app", "frontend-pdb", map[string]string{"tier": "frontend"}, 1, 2),
	}
	return fakek8s.NewSimpleClientset(append(objs, extra...)...)
}

func TestCheckPodDisruptionBudgets_MultiPDBPodScoped(t *testing.T) {
	hc := NewChecker(nil, overlappingPDBCluster(), nil, nil)
	hc.SetTargetNodegroups([]string{"ng-a"})
	result := hc.CheckPodDisruptionBudgets(context.Background())

	if result.Status != StatusWarn {
		t.Fatalf("status = %s, want WARN (msg: %s)", result.Status, result.Message)
	}
	if !strings.Contains(result.Message, "1 pod(s) are covered by more than one PDB") || !strings.Contains(result.Message, "stall on eviction") {
		t.Errorf("message = %q", result.Message)
	}
	want := "pod my-app/web-1 is covered by 2 PDBs (frontend-pdb, web-pdb); the eviction API refuses such pods"
	if !hasDetail(result.Details, want) {
		t.Errorf("details missing %q: %v", want, result.Details)
	}
	if hasDetail(result.Details, "web-2") {
		t.Errorf("web-2 runs on ng-b and must not be reported: %v", result.Details)
	}
}

func TestCheckPodDisruptionBudgets_MultiPDBPodUnscoped(t *testing.T) {
	result := NewChecker(nil, overlappingPDBCluster(), nil, nil).CheckPodDisruptionBudgets(context.Background())
	if result.Status != StatusWarn || !strings.Contains(result.Message, "2 pod(s) are covered by more than one PDB") ||
		!strings.Contains(result.Message, "may block a drain") {
		t.Fatalf("status = %s, message = %q", result.Status, result.Message)
	}
	if !hasDetail(result.Details, "my-app/web-1") || !hasDetail(result.Details, "my-app/web-2") {
		t.Errorf("both pods should be reported: %v", result.Details)
	}
}

func TestCheckPodDisruptionBudgets_MultiPDBPodGating(t *testing.T) {
	alwaysAllow := policyv1.AlwaysAllow
	web := map[string]string{"app": "web", "tier": "frontend"}

	// The eviction API refuses a pod with several PDBs before it looks at
	// the unhealthy-pod policy, so a not-Ready pod still blocks.
	notReady := labeledPod("my-app", "web-3", "node-a1", web)
	notReady.Status.Conditions[0].Status = corev1.ConditionFalse
	// Terminal and pending pods are deleted without a PDB check.
	done := labeledPod("my-app", "web-4", "node-a1", web)
	done.Status.Phase = corev1.PodSucceeded
	pending := labeledPod("my-app", "web-5", "node-a1", web)
	pending.Status.Phase = corev1.PodPending
	// A pod that only one PDB selects is not a multi-PDB pod.
	single := labeledPod("my-app", "api-1", "node-a1", map[string]string{"app": "web"})

	client := overlappingPDBCluster(notReady, done, pending, single)
	pdbs, _ := client.PolicyV1().PodDisruptionBudgets("my-app").List(context.Background(), metav1.ListOptions{})
	for i := range pdbs.Items {
		pdbs.Items[i].Spec.UnhealthyPodEvictionPolicy = &alwaysAllow
	}

	hc := NewChecker(nil, client, nil, nil)
	got := hc.findMultiPDBPods(context.Background(), pdbs.Items, map[string]bool{"node-a1": true}, map[string][]corev1.Pod{})
	var names []string
	for _, p := range got {
		names = append(names, p.Name)
	}
	if fmt.Sprint(names) != "[web-1 web-3]" {
		t.Errorf("multi-PDB pods = %v, want [web-1 web-3]", names)
	}
}

func TestDrainBlockers_ReportsMultiPDBPods(t *testing.T) {
	hc := NewChecker(nil, overlappingPDBCluster(), nil, nil)
	report, err := hc.DrainBlockers(context.Background(), "prod", []string{"ng-a"})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Blockers) != 0 || len(report.MultiPDBPods) != 1 || report.MultiPDBPods[0].Name != "web-1" {
		t.Errorf("report = %+v", report)
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// ScaleDownBlockers: worst-case pod loss per PDB
// ──────────────────────────────────────────────────────────────────────────────

func TestScaleDownBlockers(t *testing.T) {
	web := func(name, node string) *corev1.Pod { return appPod("my-app", name, "web", node) }
	cases := []struct {
		name     string
		objs     []runtime.Object
		remove   int32
		wantLoss int32 // 0 means no blocker
	}{
		{
			name:   "1 allowed, pods on 2 nodes, remove 2",
			objs:   []runtime.Object{web("w1", "n1"), web("w2", "n2"), pdbAllowing("my-app", "web-pdb", "web", 1, 2)},
			remove: 2, wantLoss: 2,
		},
		{
			name:   "1 allowed, pods on 2 nodes, remove 1",
			objs:   []runtime.Object{web("w1", "n1"), web("w2", "n2"), pdbAllowing("my-app", "web-pdb", "web", 1, 2)},
			remove: 1,
		},
		{
			name:   "1 allowed, both pods on one node, remove 1",
			objs:   []runtime.Object{web("w1", "n1"), web("w2", "n1"), pdbAllowing("my-app", "web-pdb", "web", 1, 2)},
			remove: 1, wantLoss: 2,
		},
		{
			name:   "2 allowed, pods on 2 nodes, remove 3",
			objs:   []runtime.Object{web("w1", "n1"), web("w2", "n2"), web("w3", "o1"), pdbAllowing("my-app", "web-pdb", "web", 2, 3)},
			remove: 3,
		},
		{
			name:   "0 allowed, one pod on the nodegroup",
			objs:   []runtime.Object{web("w1", "n3"), web("w2", "o1"), pdbAllowing("my-app", "web-pdb", "web", 0, 2)},
			remove: 1, wantLoss: 1,
		},
		{
			name:   "0 allowed, pods only on another nodegroup",
			objs:   []runtime.Object{web("w1", "o1"), pdbAllowing("my-app", "web-pdb", "web", 0, 1)},
			remove: 2,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			objs := append([]runtime.Object{
				ngNode("n1", "workers"), ngNode("n2", "workers"), ngNode("n3", "workers"), ngNode("o1", "other"),
			}, tc.objs...)
			hc := NewChecker(nil, fakek8s.NewSimpleClientset(objs...), nil, nil)
			report, err := hc.ScaleDownBlockers(context.Background(), "prod", "workers", tc.remove)
			if err != nil {
				t.Fatal(err)
			}
			if !report.Scoped {
				t.Errorf("report should be scoped")
			}
			if tc.wantLoss == 0 {
				if len(report.Blockers) != 0 {
					t.Errorf("want no blockers, got %+v", report.Blockers)
				}
				return
			}
			if len(report.Blockers) != 1 || report.Blockers[0].ScaleDownLoss != tc.wantLoss || report.Blockers[0].ScaleDownNodes != tc.remove {
				t.Fatalf("want web-pdb with loss %d, got %+v", tc.wantLoss, report.Blockers)
			}
		})
	}
}

func TestScaleDownBlockers_SummaryShowsArithmetic(t *testing.T) {
	hc := NewChecker(nil, fakek8s.NewSimpleClientset(
		ngNode("n1", "workers"), ngNode("n2", "workers"),
		appPod("my-app", "w1", "web", "n1"), appPod("my-app", "w2", "web", "n2"),
		pdbAllowing("my-app", "web-pdb", "web", 1, 2),
	), nil, nil)
	report, err := hc.ScaleDownBlockers(context.Background(), "prod", "workers", 2)
	if err != nil || len(report.Blockers) != 1 {
		t.Fatalf("report = %+v, err = %v", report, err)
	}
	want := "my-app/web-pdb (2/2 pods healthy, 1 disruption(s) allowed; covered pods run on 2 of the nodegroup's nodes, so removing 2 node(s) can take down 2 pod(s), more than the 1 allowed)"
	if got := report.Blockers[0].DrainBlockerSummary(); got != want {
		t.Errorf("summary =\n %q\nwant\n %q", got, want)
	}
}

func TestScaleDownBlockers_PodListFailureFailsClosed(t *testing.T) {
	client := fakek8s.NewSimpleClientset(
		ngNode("n1", "workers"),
		pdbAllowing("my-app", "web-pdb", "web", 1, 2),
	)
	client.PrependReactor("list", "pods", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, fmt.Errorf("forbidden")
	})
	_, err := NewChecker(nil, client, nil, nil).ScaleDownBlockers(context.Background(), "prod", "workers", 1)
	if err == nil || !strings.Contains(err.Error(), "forbidden") {
		t.Errorf("want a pod-list error, got %v", err)
	}
}

func TestScaleDownBlockers_NoNodesFallsBackToZeroDisruptionPDBs(t *testing.T) {
	// No node carries the workers label: fall back to every PDB that allows 0.
	hc := NewChecker(nil, fakek8s.NewSimpleClientset(
		pdbAllowing("my-app", "stuck-pdb", "web", 0, 1),
		pdbAllowing("my-app", "ok-pdb", "api", 1, 2),
	), nil, nil)
	report, err := hc.ScaleDownBlockers(context.Background(), "prod", "workers", 1)
	if err != nil {
		t.Fatal(err)
	}
	if report.Scoped || len(report.Blockers) != 1 || report.Blockers[0].Name != "stuck-pdb" {
		t.Errorf("report = %+v", report)
	}
}

func TestScaleDownBlockers_NoKubeClient(t *testing.T) {
	if _, err := NewChecker(nil, nil, nil, nil).ScaleDownBlockers(context.Background(), "prod", "workers", 1); !errors.Is(err, ErrNoKubeClient) {
		t.Errorf("err = %v, want ErrNoKubeClient", err)
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// CheckNodeHealth: minimum ready ratio
// ──────────────────────────────────────────────────────────────────────────────

func nodeHealthChecker(k8s *fakek8s.Clientset) *HealthChecker {
	api := mocks.NewEKSAPI().WithCluster("prod", "1.32").
		WithNodegroup("ng-a", "1.32", ekstypes.AMITypesAl2023X8664Standard).Build()
	hc := &HealthChecker{eksClient: api}
	if k8s != nil {
		hc.k8sClient = k8s
	}
	return hc
}

func nodesWithReady(ready, notReady int) *fakek8s.Clientset {
	var objs []runtime.Object
	for i := range ready {
		objs = append(objs, readyNode(fmt.Sprintf("ready-%d", i)))
	}
	for i := range notReady {
		objs = append(objs, notReadyNode(fmt.Sprintf("notready-%d", i)))
	}
	return fakek8s.NewSimpleClientset(objs...)
}

func TestCheckNodeHealth_ReadyRatio(t *testing.T) {
	cases := []struct {
		name            string
		ready, notReady int
		want            HealthStatus
		wantMsg         string
	}{
		{"all ready", 4, 0, StatusPass, "4/4 nodes ready"},
		{"one NotReady", 9, 1, StatusWarn, "9/10 nodes ready (fails below 50% ready)"},
		{"exactly half ready", 5, 5, StatusWarn, "5/10 nodes ready"},
		{"most NotReady", 1, 9, StatusFail, "1/10 nodes ready, below the 50% ready minimum"},
		{"none ready", 0, 3, StatusFail, "No ready nodes"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := nodeHealthChecker(nodesWithReady(tc.ready, tc.notReady)).CheckNodeHealth(context.Background(), "prod")
			if r.Status != tc.want || !strings.Contains(r.Message, tc.wantMsg) {
				t.Errorf("status = %s, message = %q; want %s containing %q", r.Status, r.Message, tc.want, tc.wantMsg)
			}
			if !r.IsBlocking {
				t.Errorf("Node Health must stay blocking")
			}
		})
	}
}

func TestCheckNodeHealth_EstimatedStaysWarn(t *testing.T) {
	// Without Kubernetes access 1 of 10 nodes looks ready (a DEGRADED
	// nodegroup holds the other 9). The estimate is too coarse to fail on.
	api := &mocks.EKSAPI{
		ListNodegroupsFn: func(context.Context, *eks.ListNodegroupsInput, ...func(*eks.Options)) (*eks.ListNodegroupsOutput, error) {
			return &eks.ListNodegroupsOutput{Nodegroups: []string{"ng-a", "ng-b"}}, nil
		},
		DescribeNodegroupFn: func(_ context.Context, in *eks.DescribeNodegroupInput, _ ...func(*eks.Options)) (*eks.DescribeNodegroupOutput, error) {
			status, size := ekstypes.NodegroupStatusActive, int32(1)
			if aws.ToString(in.NodegroupName) == "ng-b" {
				status, size = ekstypes.NodegroupStatusDegraded, 9
			}
			return &eks.DescribeNodegroupOutput{Nodegroup: &ekstypes.Nodegroup{
				NodegroupName: in.NodegroupName,
				Status:        status,
				ScalingConfig: &ekstypes.NodegroupScalingConfig{DesiredSize: aws.Int32(size)},
			}}, nil
		},
	}
	r := (&HealthChecker{eksClient: api}).CheckNodeHealth(context.Background(), "prod")
	if r.Status != StatusWarn || !strings.Contains(r.Message, "(estimated)") {
		t.Errorf("status = %s, message = %q; want an estimated WARN", r.Status, r.Message)
	}
}
