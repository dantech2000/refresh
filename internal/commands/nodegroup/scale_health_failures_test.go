package nodegroup

import (
	"context"
	"errors"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	fakek8s "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/dantech2000/refresh/internal/health"
	"github.com/dantech2000/refresh/internal/mocks/fakeaws"
)

// withScaleKube makes `nodegroup scale` use kube for its health check.
func withScaleKube(t *testing.T, kube kubernetes.Interface) {
	t.Helper()
	orig := scaleKubeClientFn
	scaleKubeClientFn = func(context.Context, health.ClusterDescriber, string, string, string, string, bool) (kubernetes.Interface, health.KubeSelection) {
		return kube, health.KubeSelection{}
	}
	t.Cleanup(func() { scaleKubeClientFn = orig })
}

// A health check that could not list the critical pods is missing data:
// `nodegroup scale --health-check` lists the read in failures, names it on
// stderr, and exits 4 after the scale. It was exit 0 with "failures": [].
func TestScale_HealthCheckUnreadPodsExitsFour(t *testing.T) {
	ready := corev1.NodeStatus{Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}}}
	node := func(name string) *corev1.Node {
		return &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: map[string]string{"eks.amazonaws.com/nodegroup": "ng-a"}}, Status: ready}
	}
	kube := fakek8s.NewClientset(node("n1"), node("n2"), node("n3"))
	kube.PrependReactor("list", "pods", func(action k8stesting.Action) (bool, runtime.Object, error) {
		if action.GetNamespace() == "kube-system" {
			return true, nil, errors.New("pods is forbidden")
		}
		return false, nil, nil
	})
	withScaleKube(t, kube)
	srv := fakeaws.New(t, prodCluster(&fakeaws.Nodegroup{Name: "ng-a", Version: "1.31", Desired: 3, Min: 1, Max: 5}))

	stdout, stderr, err := runNodegroup(t, "scale", "prod", "-n", "ng-a", "--desired", "4", "--yes", "--health-check", "-o", "json")
	if code := exitCodeOf(err); code != 4 {
		t.Fatalf("exit code = %d (err %v), want 4\nstderr:\n%s", code, err, stderr)
	}
	doc := fakeaws.RequireOneDocument(t, "json", stdout).(map[string]any)
	found := 0
	for _, f := range doc["failures"].([]any) {
		f := f.(map[string]any)
		if e, _ := f["error"].(string); strings.Contains(e, "listing pods in kube-system") {
			found++
			if f["kind"] != "Cluster" || f["name"] != "prod" || f["region"] != "us-east-1" {
				t.Errorf("failure = %v, want a cluster failure for prod in us-east-1", f)
			}
		}
	}
	if found != 1 {
		t.Errorf("failures = %v, want the failed pod list once (the pre- and post-scaling checks both hit it)", doc["failures"])
	}
	if doc["outcome"] != "Requested" || !calledPath(srv, "/update-config") {
		t.Errorf("outcome = %v, want Requested: a warning does not stop the scale", doc["outcome"])
	}
	if n := strings.Count(stderr, "listing pods in kube-system"); n < 1 || !strings.Contains(stderr, "warning: cluster prod (us-east-1): ") {
		t.Errorf("stderr does not report the failed read:\n%s", stderr)
	}
}
