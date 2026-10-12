package health

import (
	"context"
	"errors"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/runtime"
	fakek8s "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/dantech2000/refresh/internal/diag"
)

// failPodLists makes the pod list of each named namespace fail.
func failPodLists(client *fakek8s.Clientset, namespaces ...string) {
	client.PrependReactor("list", "pods", func(action k8stesting.Action) (bool, runtime.Object, error) {
		for _, ns := range namespaces {
			if action.GetNamespace() == ns {
				return true, nil, errors.New("pods is forbidden")
			}
		}
		return false, nil, nil
	})
}

// A pod list that fails in every critical namespace is unread data. It must
// not read as "No critical pods found".
func TestCheckCriticalWorkloads_AllListsFail(t *testing.T) {
	client := fakek8s.NewSimpleClientset(allReadyPod("kube-system", "coredns-1"))
	failPodLists(client, "kube-system", "kube-public", "kube-node-lease")

	r := NewChecker(nil, client, nil, nil).checkCriticalWorkloads(context.Background(), "prod")

	if r.Status != StatusWarn || r.Skipped {
		t.Errorf("status = %s (skipped %v), want a measured WARN", r.Status, r.Skipped)
	}
	if strings.Contains(r.Message, "No critical pods found") || !strings.Contains(r.Message, "Failed to list pods") {
		t.Errorf("message = %q, want a failed-list message", r.Message)
	}
	if len(r.failures) != 3 {
		t.Fatalf("failures = %d, want 3 (one per namespace): %+v", len(r.failures), r.failures)
	}
	for _, f := range r.failures {
		if f.Kind != diag.KindCluster || f.Name != "prod" || !strings.Contains(f.Error, "listing pods in kube-") {
			t.Errorf("failure = %+v, want a cluster failure for prod that names the namespace", f)
		}
	}
}

// A partial read cannot pass: the pods of the unread namespace are unknown.
func TestCheckCriticalWorkloads_PartialListFailureCannotPass(t *testing.T) {
	client := fakek8s.NewSimpleClientset(allReadyPod("kube-public", "ok-1"))
	failPodLists(client, "kube-system")

	r := NewChecker(nil, client, nil, nil).checkCriticalWorkloads(context.Background(), "prod")

	if r.Status != StatusWarn {
		t.Errorf("status = %s, want WARN", r.Status)
	}
	if !strings.Contains(r.Message, "1/1 critical pods running") || !strings.Contains(r.Message, "kube-system") {
		t.Errorf("message = %q, want the read count and the unread namespace", r.Message)
	}
	if len(r.failures) != 1 {
		t.Errorf("failures = %d, want 1: %+v", len(r.failures), r.failures)
	}
}

// Pods that were read and are failing still fail the check when another
// namespace could not be read.
func TestCheckCriticalWorkloads_PartialListFailureKeepsFail(t *testing.T) {
	client := fakek8s.NewSimpleClientset(pendingPod("kube-public", "stuck-1"))
	failPodLists(client, "kube-system")

	r := NewChecker(nil, client, nil, nil).checkCriticalWorkloads(context.Background(), "prod")

	if r.Status != StatusFail || len(r.failures) != 1 {
		t.Errorf("status = %s with %d failures, want FAIL with 1 failure", r.Status, len(r.failures))
	}
}

// The unread pods reach the summary: the decision warns and lists the read.
func TestRunAllChecks_UnreadCriticalPodsWarn(t *testing.T) {
	client := fakek8s.NewSimpleClientset(allReadyPod("kube-public", "ok-1"))
	failPodLists(client, "kube-system")

	s := aggregateResults([]HealthResult{
		NewChecker(nil, client, nil, nil).checkCriticalWorkloads(context.Background(), "prod"),
	})

	if s.Decision != DecisionWarn || len(s.Failures) != 1 {
		t.Errorf("decision = %s with %d failures, want Warn with 1 failure", s.Decision, len(s.Failures))
	}
}
