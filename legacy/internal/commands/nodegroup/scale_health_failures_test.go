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

	"github.com/dantech2000/refresh/legacy/internal/health"
	"github.com/dantech2000/refresh/legacy/internal/mocks/fakeaws"
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

// scaleKube is a cluster with three nodes of ng-a. The pod list of
// kube-system fails while failPods() is true, and the nodes are NotReady
// while notReady() is true. Both are asked on every read, so a test can
// change the answer between the pre- and the post-scaling check.
func scaleKube(failPods, notReady func() bool) kubernetes.Interface {
	kube := fakek8s.NewClientset()
	kube.PrependReactor("list", "nodes", func(k8stesting.Action) (bool, runtime.Object, error) {
		status := corev1.ConditionTrue
		if notReady() {
			status = corev1.ConditionFalse
		}
		list := &corev1.NodeList{}
		for _, name := range []string{"n1", "n2", "n3"} {
			list.Items = append(list.Items, corev1.Node{
				ObjectMeta: metav1.ObjectMeta{Name: name, Labels: map[string]string{"eks.amazonaws.com/nodegroup": "ng-a"}},
				Status:     corev1.NodeStatus{Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: status}}},
			})
		}
		return true, list, nil
	})
	kube.PrependReactor("list", "pods", func(action k8stesting.Action) (bool, runtime.Object, error) {
		if action.GetNamespace() == "kube-system" && failPods() {
			return true, nil, errors.New("pods is forbidden")
		}
		return false, nil, nil
	})
	return kube
}

// A health check that could not list the critical pods is missing data:
// `nodegroup scale --health-check` lists the read in failures, names it on
// stderr, and exits 4 after the scale. It was exit 0 with "failures": [].
// The read is taken from the pre-scaling check and from the post-scaling
// check: each case fails it in one of them, or in both (listed once). A
// blocked precheck (exit 3) and a failed post-scaling check (exit 5) keep
// their exit code and still list the read.
func TestScale_HealthCheckUnreadPods(t *testing.T) {
	always, never := func() bool { return true }, func() bool { return false }
	for _, tc := range []struct {
		name string
		// failPods and notReady get scaled(): whether EKS has the request.
		failPods, notReady func(scaled func() bool) func() bool
		wantExit           int
		wantOutcome        string
		wantScaled         bool
	}{
		{"both checks", func(func() bool) func() bool { return always }, func(func() bool) func() bool { return never }, 4, "Requested", true},
		{"pre-scaling check only", func(scaled func() bool) func() bool { return func() bool { return !scaled() } }, func(func() bool) func() bool { return never }, 4, "Requested", true},
		{"post-scaling check only", func(scaled func() bool) func() bool { return scaled }, func(func() bool) func() bool { return never }, 4, "Requested", true},
		{"blocked precheck", func(func() bool) func() bool { return always }, func(func() bool) func() bool { return always }, 3, "Blocked", false},
		{"failed post-scaling check", func(scaled func() bool) func() bool { return scaled }, func(scaled func() bool) func() bool { return scaled }, 5, "CompletedWithIssues", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := fakeaws.New(t, prodCluster(&fakeaws.Nodegroup{Name: "ng-a", Version: "1.31", Desired: 3, Min: 1, Max: 5}))
			scaled := func() bool { return calledPath(srv, "/update-config") }
			withScaleKube(t, scaleKube(tc.failPods(scaled), tc.notReady(scaled)))

			stdout, stderr, err := runNodegroup(t, "scale", "prod", "-n", "ng-a", "--desired", "4", "--yes", "--health-check", "-o", "json")
			if code := exitCodeOf(err); code != tc.wantExit {
				t.Fatalf("exit code = %d (err %v), want %d\nstderr:\n%s", code, err, tc.wantExit, stderr)
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
				t.Errorf("failures = %v, want the failed pod list once", doc["failures"])
			}
			if doc["outcome"] != tc.wantOutcome || scaled() != tc.wantScaled {
				t.Errorf("outcome = %v (scaled %v), want %s (scaled %v)", doc["outcome"], scaled(), tc.wantOutcome, tc.wantScaled)
			}
			if !strings.Contains(stderr, "warning: cluster prod (us-east-1): ") || !strings.Contains(stderr, "listing pods in kube-system") {
				t.Errorf("stderr does not report the failed read:\n%s", stderr)
			}
		})
	}
}
