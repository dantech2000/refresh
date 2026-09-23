package nodegroup

import (
	"context"
	"errors"
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

	"github.com/dantech2000/refresh/internal/health"
	"github.com/dantech2000/refresh/internal/mocks"
)

func gateNode(name, ng string) *corev1.Node {
	return &corev1.Node{ObjectMeta: metav1.ObjectMeta{
		Name:   name,
		Labels: map[string]string{"eks.amazonaws.com/nodegroup": ng},
	}}
}

func gatePod(ns, name, app, node string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name, Labels: map[string]string{"app": app}},
		Spec:       corev1.PodSpec{NodeName: node},
		Status: corev1.PodStatus{
			Phase:      corev1.PodRunning,
			Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}},
		},
	}
}

// gatePDB returns a PDB over app=<app> that allows `allowed` disruptions out
// of `expected` pods.
func gatePDB(ns, name, app string, allowed, expected int32) *policyv1.PodDisruptionBudget {
	return &policyv1.PodDisruptionBudget{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name},
		Spec: policyv1.PodDisruptionBudgetSpec{
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": app}},
		},
		Status: policyv1.PodDisruptionBudgetStatus{
			DisruptionsAllowed: allowed,
			CurrentHealthy:     expected,
			DesiredHealthy:     expected,
			ExpectedPods:       expected,
		},
	}
}

// scaleGateService returns a service whose nodegroup "workers" has desired
// size current, wired to a health checker over the given Kubernetes objects
// (no Kubernetes client when objs is nil).
func scaleGateService(current int32, objs []runtime.Object) (*ServiceImpl, *mocks.EKSAPI) {
	api := &mocks.EKSAPI{
		DescribeNodegroupFn: func(_ context.Context, in *eks.DescribeNodegroupInput, _ ...func(*eks.Options)) (*eks.DescribeNodegroupOutput, error) {
			return &eks.DescribeNodegroupOutput{Nodegroup: &ekstypes.Nodegroup{
				NodegroupName: in.NodegroupName,
				Status:        ekstypes.NodegroupStatusActive,
				ScalingConfig: &ekstypes.NodegroupScalingConfig{DesiredSize: aws.Int32(current), MinSize: aws.Int32(0), MaxSize: aws.Int32(10)},
			}}, nil
		},
		UpdateNodegroupConfigFn: func(context.Context, *eks.UpdateNodegroupConfigInput, ...func(*eks.Options)) (*eks.UpdateNodegroupConfigOutput, error) {
			return &eks.UpdateNodegroupConfigOutput{}, nil
		},
	}
	svc := newTestService(api)
	if objs != nil {
		svc.healthChecker = health.NewChecker(nil, fakek8s.NewClientset(objs...), nil, nil)
	} else {
		svc.healthChecker = health.NewChecker(nil, nil, nil, nil)
	}
	return svc, api
}

// blockedCluster has web-pdb (0 disruptions) covering a pod on a "workers"
// node, and batch-pdb (0 disruptions) covering a pod on another nodegroup.
func blockedCluster() []runtime.Object {
	return []runtime.Object{
		gateNode("w1", "workers"),
		gateNode("o1", "other"),
		gatePod("app", "web-1", "web", "w1"),
		gatePod("app", "batch-1", "batch", "o1"),
		gatePDB("app", "web-pdb", "web", 0, 1),
		gatePDB("app", "batch-pdb", "batch", 0, 1),
	}
}

func TestScale_CheckPDBsRefusesBlockedScaleDown(t *testing.T) {
	svc, api := scaleGateService(3, blockedCluster())

	err := svc.Scale(context.Background(), "prod", "workers", aws.Int32(1), nil, nil, ScaleOptions{CheckPDBs: true})
	var blocked *ScaleDownBlockedError
	if !errors.As(err, &blocked) {
		t.Fatalf("want *ScaleDownBlockedError, got %v", err)
	}
	if api.Calls.UpdateNodegroupConfig != 0 {
		t.Fatalf("UpdateNodegroupConfig called %d times; a refused scale must not mutate", api.Calls.UpdateNodegroupConfig)
	}
	if !blocked.Check.Scoped || len(blocked.Check.Blockers) != 1 || blocked.Check.Blockers[0].Name != "web-pdb" {
		t.Errorf("want only web-pdb as a scoped blocker, got %+v", blocked.Check)
	}
	msg := err.Error()
	for _, want := range []string{"refusing to scale prod/workers down from 3 to 1", "app/web-pdb", "--force"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error missing %q:\n%s", want, msg)
		}
	}
	if strings.Contains(msg, "batch-pdb") {
		t.Errorf("batch-pdb has no pods on workers and must not be listed:\n%s", msg)
	}
}

func TestScale_CheckPDBsForceProceeds(t *testing.T) {
	svc, api := scaleGateService(3, blockedCluster())

	err := svc.Scale(context.Background(), "prod", "workers", aws.Int32(1), nil, nil, ScaleOptions{CheckPDBs: true, Force: true})
	if err != nil {
		t.Fatalf("--force should scale anyway, got %v", err)
	}
	if api.Calls.UpdateNodegroupConfig != 1 {
		t.Errorf("UpdateNodegroupConfig called %d times, want 1", api.Calls.UpdateNodegroupConfig)
	}
}

func TestScale_CheckPDBsAllowsUnblockedScaleDown(t *testing.T) {
	// Only batch-pdb allows 0 disruptions, and its pod runs on another nodegroup.
	svc, api := scaleGateService(3, []runtime.Object{
		gateNode("w1", "workers"),
		gateNode("o1", "other"),
		gatePod("app", "web-1", "web", "w1"),
		gatePod("app", "batch-1", "batch", "o1"),
		gatePDB("app", "web-pdb", "web", 1, 1),
		gatePDB("app", "batch-pdb", "batch", 0, 1),
	})

	if err := svc.Scale(context.Background(), "prod", "workers", aws.Int32(1), nil, nil, ScaleOptions{CheckPDBs: true}); err != nil {
		t.Fatalf("unblocked scale-down should proceed, got %v", err)
	}
	if api.Calls.UpdateNodegroupConfig != 1 {
		t.Errorf("UpdateNodegroupConfig called %d times, want 1", api.Calls.UpdateNodegroupConfig)
	}
}

func TestScale_CheckPDBsIgnoresScaleUp(t *testing.T) {
	svc, api := scaleGateService(1, blockedCluster())

	if err := svc.Scale(context.Background(), "prod", "workers", aws.Int32(3), nil, nil, ScaleOptions{CheckPDBs: true}); err != nil {
		t.Fatalf("a scale-up is never refused, got %v", err)
	}
	if api.Calls.UpdateNodegroupConfig != 1 {
		t.Errorf("UpdateNodegroupConfig called %d times, want 1", api.Calls.UpdateNodegroupConfig)
	}
}

func TestScale_CheckPDBsWithoutKubeClientRefuses(t *testing.T) {
	svc, api := scaleGateService(3, nil)

	err := svc.Scale(context.Background(), "prod", "workers", aws.Int32(1), nil, nil, ScaleOptions{CheckPDBs: true})
	if err == nil || !errors.Is(err, health.ErrNoKubeClient) {
		t.Fatalf("want an ErrNoKubeClient refusal, got %v", err)
	}
	if !strings.Contains(err.Error(), "--force") {
		t.Errorf("the refusal should point at --force, got %v", err)
	}
	if api.Calls.UpdateNodegroupConfig != 0 {
		t.Errorf("UpdateNodegroupConfig called %d times, want 0", api.Calls.UpdateNodegroupConfig)
	}
}

func TestCheckScaleDownPDBs(t *testing.T) {
	svc, _ := scaleGateService(3, blockedCluster())

	check, err := svc.CheckScaleDownPDBs(context.Background(), "prod", "workers", aws.Int32(1), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !check.ScaleDown || !check.Refused() || check.CurrentDesired != 3 || check.RequestedDesired != 1 {
		t.Errorf("unexpected check: %+v", check)
	}

	check, err = svc.CheckScaleDownPDBs(context.Background(), "prod", "workers", nil, nil, nil)
	if err != nil || check.ScaleDown || check.Refused() {
		t.Errorf("nil desired is never a scale-down, got %+v, %v", check, err)
	}
}

// spreadCluster has web-pdb allowing 1 disruption of 2 web pods, one pod on
// each of two "workers" nodes (w1, w2). A third node (w3) runs nothing.
func spreadCluster() []runtime.Object {
	return []runtime.Object{
		gateNode("w1", "workers"),
		gateNode("w2", "workers"),
		gateNode("w3", "workers"),
		gatePod("app", "web-1", "web", "w1"),
		gatePod("app", "web-2", "web", "w2"),
		gatePDB("app", "web-pdb", "web", 1, 2),
	}
}

func TestScale_CheckPDBsRefusesWorstCaseOverBudget(t *testing.T) {
	// Removing 2 of 3 nodes: the ASG may pick w1 and w2, which takes down
	// both web pods although web-pdb allows only 1.
	svc, api := scaleGateService(3, spreadCluster())

	err := svc.Scale(context.Background(), "prod", "workers", aws.Int32(1), nil, nil, ScaleOptions{CheckPDBs: true})
	var blocked *ScaleDownBlockedError
	if !errors.As(err, &blocked) {
		t.Fatalf("want *ScaleDownBlockedError, got %v", err)
	}
	if api.Calls.UpdateNodegroupConfig != 0 {
		t.Fatalf("UpdateNodegroupConfig called %d times; a refused scale must not mutate", api.Calls.UpdateNodegroupConfig)
	}
	msg := err.Error()
	for _, want := range []string{
		"1 PodDisruptionBudget(s) could lose more pods on this nodegroup's nodes than they allow",
		"app/web-pdb (2/2 pods healthy, 1 disruption(s) allowed; covered pods run on 2 of the nodegroup's nodes, so removing 2 node(s) can take down 2 pod(s), more than the 1 allowed)",
		"--force",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("error missing %q:\n%s", want, msg)
		}
	}
}

func TestScale_CheckPDBsAllowsWorstCaseWithinBudget(t *testing.T) {
	// Removing 1 of 3 nodes takes down at most 1 web pod, which web-pdb allows.
	svc, api := scaleGateService(3, spreadCluster())

	if err := svc.Scale(context.Background(), "prod", "workers", aws.Int32(2), nil, nil, ScaleOptions{CheckPDBs: true}); err != nil {
		t.Fatalf("a scale-down within the PDB budget should proceed, got %v", err)
	}
	if api.Calls.UpdateNodegroupConfig != 1 {
		t.Errorf("UpdateNodegroupConfig called %d times, want 1", api.Calls.UpdateNodegroupConfig)
	}
}

func TestScale_CheckPDBsWorstCaseForceProceeds(t *testing.T) {
	svc, api := scaleGateService(3, spreadCluster())

	if err := svc.Scale(context.Background(), "prod", "workers", aws.Int32(1), nil, nil, ScaleOptions{CheckPDBs: true, Force: true}); err != nil {
		t.Fatalf("--force should scale anyway, got %v", err)
	}
	if api.Calls.UpdateNodegroupConfig != 1 {
		t.Errorf("UpdateNodegroupConfig called %d times, want 1", api.Calls.UpdateNodegroupConfig)
	}
}

func TestScale_CheckPDBsPodListFailureRefuses(t *testing.T) {
	svc, api := scaleGateService(3, nil)
	client := fakek8s.NewClientset(spreadCluster()...)
	client.PrependReactor("list", "pods", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("pods is forbidden")
	})
	svc.healthChecker = health.NewChecker(nil, client, nil, nil)

	err := svc.Scale(context.Background(), "prod", "workers", aws.Int32(1), nil, nil, ScaleOptions{CheckPDBs: true})
	if err == nil || !strings.Contains(err.Error(), "pods is forbidden") || !strings.Contains(err.Error(), "--force") {
		t.Fatalf("want a fail-closed refusal naming the error and --force, got %v", err)
	}
	if api.Calls.UpdateNodegroupConfig != 0 {
		t.Errorf("UpdateNodegroupConfig called %d times, want 0", api.Calls.UpdateNodegroupConfig)
	}
}
