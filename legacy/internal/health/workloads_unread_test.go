package health

import (
	"context"
	"errors"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/runtime"
	fakek8s "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/dantech2000/refresh/legacy/internal/diag"
	"github.com/dantech2000/refresh/legacy/internal/mocks"
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

// The score is at most 60 while a namespace is unread, whatever the pods
// that were read say. The status of those pods is kept.
func TestCheckCriticalWorkloads_UnreadNamespaceCapsTheScore(t *testing.T) {
	healthy := func(n int) []runtime.Object {
		var out []runtime.Object
		for i := range n {
			out = append(out, allReadyPod("kube-public", "ok-"+string(rune('a'+i))))
		}
		return out
	}
	for _, tc := range []struct {
		name       string
		pods       []runtime.Object
		wantStatus HealthStatus
		wantScore  int
	}{
		{"readable pods pass", healthy(10), StatusWarn, 60},
		{"readable pods warn", append(healthy(9), pendingPod("kube-public", "stuck-1")), StatusWarn, 60},
		{"readable pods fail", append(healthy(1), pendingPod("kube-public", "stuck-1")), StatusFail, 50},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := fakek8s.NewSimpleClientset(tc.pods...)
			failPodLists(client, "kube-system")

			r := NewChecker(nil, client, nil, nil).checkCriticalWorkloads(context.Background(), "prod")

			if r.Status != tc.wantStatus || r.Score != tc.wantScore {
				t.Errorf("status = %s, score = %d; want %s, %d", r.Status, r.Score, tc.wantStatus, tc.wantScore)
			}
			if !strings.Contains(r.Message, "failed to list pods in kube-system") || len(r.failures) != 1 {
				t.Errorf("message = %q with %d failures, want the unread namespace and 1 failure", r.Message, len(r.failures))
			}
		})
	}
}

// RunAllChecks carries the unread pods to the summary: the check warns, and
// the failure names the cluster and the checker's region. This fails if
// RunAllChecks stops passing the cluster name to the check.
func TestRunAllChecks_UnreadCriticalPodsWarn(t *testing.T) {
	client := fakek8s.NewSimpleClientset(allReadyPod("kube-public", "ok-1"))
	failPodLists(client, "kube-system")
	api := mocks.NewEKSAPI().WithCluster("prod", "1.32").Build()
	hc := &HealthChecker{eksClient: api, k8sClient: client, region: "eu-west-1"}

	s := hc.RunAllChecks(context.Background(), "prod")

	if s.Decision == DecisionProceed {
		t.Errorf("decision = %s, want a warning or a block", s.Decision)
	}
	warned := false
	for _, w := range s.Warnings {
		warned = warned || strings.Contains(w, "failed to list pods in kube-system")
	}
	if !warned {
		t.Errorf("warnings = %v, want the Critical Workloads warning", s.Warnings)
	}
	var got []diag.Failure
	for _, f := range s.Failures {
		if strings.Contains(f.Error, "listing pods in kube-system") {
			got = append(got, f)
		}
	}
	if len(got) != 1 || got[0].Kind != diag.KindCluster || got[0].Name != "prod" || got[0].Region != "eu-west-1" {
		t.Errorf("failures = %+v, want one cluster failure for prod in eu-west-1", s.Failures)
	}
}
