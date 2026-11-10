package fleet_test

import (
	"context"
	"errors"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes"

	"github.com/dantech2000/refresh/internal/apidoc"
	"github.com/dantech2000/refresh/internal/fleet"
	"github.com/dantech2000/refresh/internal/fleet/fleettest"
	"github.com/dantech2000/refresh/internal/kube/kubetest"
)

// timeoutError is a net.Error that timed out, as the client's own timeout
// reports.
type timeoutError struct{}

func (timeoutError) Error() string   { return "i/o timeout" }
func (timeoutError) Timeout() bool   { return true }
func (timeoutError) Temporary() bool { return true }

// The reason of a failed Kubernetes read follows the type of its error.
func TestKubeFailure(t *testing.T) {
	nodes := schema.GroupResource{Resource: "nodes"}
	tests := []struct {
		name string
		err  error
		want apidoc.Reason
	}{
		{"interrupted", fmt.Errorf("list: %w", context.Canceled), apidoc.ReasonInterrupted},
		{"the connection was never opened", &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}, apidoc.ReasonNetworkError},
		// Go's connect timeout is also context.DeadlineExceeded.
		{"a dial that timed out", &net.OpError{Op: "dial", Net: "tcp", Err: fmt.Errorf("i/o timeout: %w", context.DeadlineExceeded)}, apidoc.ReasonNetworkError},
		{"the deadline", fmt.Errorf("list: %w", context.DeadlineExceeded), apidoc.ReasonTimeout},
		{"forbidden", apierrors.NewForbidden(nodes, "", errors.New("RBAC")), apidoc.ReasonAccessDenied},
		{"unauthorized", apierrors.NewUnauthorized("no token"), apidoc.ReasonAccessDenied},
		{"not found", apierrors.NewNotFound(nodes, "n-1"), apidoc.ReasonNotFound},
		{"too many requests", apierrors.NewTooManyRequests("slow down", 1), apidoc.ReasonThrottled},
		{"a server timeout", apierrors.NewServerTimeout(nodes, "list", 1), apidoc.ReasonServiceError},
		{"an internal error", apierrors.NewInternalError(errors.New("etcd")), apidoc.ReasonServiceError},
		{"service unavailable", apierrors.NewServiceUnavailable("starting"), apidoc.ReasonServiceError},
		{"a timeout status", apierrors.NewTimeoutError("took too long", 1), apidoc.ReasonTimeout},
		{"another status", apierrors.NewBadRequest("bad selector"), apidoc.ReasonUnknown},
		{"the client's timeout", &net.OpError{Op: "read", Net: "tcp", Err: timeoutError{}}, apidoc.ReasonTimeout},
		{"a reset connection", &net.OpError{Op: "read", Net: "tcp", Err: errors.New("connection reset by peer")}, apidoc.ReasonNetworkError},
		{"anything else", errors.New("exec plugin failed"), apidoc.ReasonUnknown},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := fleet.KubeFailure(apidoc.FailureNodegroup, "ng-a", "listing nodes", tt.err)
			if f.Reason != tt.want {
				t.Errorf("reason = %s, want %s", f.Reason, tt.want)
			}
			if f.Kind != apidoc.FailureNodegroup || f.Name != "ng-a" || f.Operation != "" || f.Error != "listing nodes: "+tt.err.Error() {
				t.Errorf("failure = %+v", f)
			}
		})
	}
}

// kubeWorld is a world with the cluster prod: ng-a has two nodes, one of
// them not Ready, and ng-b has one. A node of no managed nodegroup and
// pods in several states run beside them.
func kubeWorld() *fleettest.World {
	pod := func(namespace, name, node, phase string) *fleettest.Pod {
		return &fleettest.Pod{Namespace: namespace, Name: name, Node: node, Phase: phase}
	}
	w := fleettest.New()
	w.Edit(func(s *fleettest.State) {
		s.Clusters = []*fleettest.Cluster{{
			Name: "prod", Version: "1.33", Region: east,
			Nodes: []*fleettest.Node{
				{Name: "a-1", Nodegroup: "ng-a"},
				{Name: "a-2", Nodegroup: "ng-a", NotReady: true},
				{Name: "b-1", Nodegroup: "ng-b"},
				{Name: "karpenter-1"},
			},
			Pods: []*fleettest.Pod{
				pod("default", "web-1", "a-1", "Running"),
				pod("default", "web-2", "a-2", "Running"),
				pod("kube-system", "coredns-1", "a-1", "Running"),
				pod("default", "job-1", "a-1", "Succeeded"),
				pod("default", "api-1", "b-1", "Running"),
				pod("default", "batch-1", "karpenter-1", "Running"),
				pod("default", "pending-1", "", "Pending"),
			},
		}}
	})
	return w
}

// kubeSnapshot is the snapshot that FetchKube fills: ng-a and ng-b were
// described, and ng-c was not.
func kubeSnapshot() *fleet.Snapshot {
	return &fleet.Snapshot{
		Cluster: fleet.Cluster{Name: "prod", Region: east},
		Nodegroups: []fleet.Nodegroup{
			{Name: "ng-a", State: fleet.Collected},
			{Name: "ng-b", State: fleet.Collected},
			{Name: "ng-c", State: fleet.Failed},
		},
	}
}

// kubeRow is what TestFetchKube compares of a nodegroup.
type kubeRow struct {
	Name                       string
	ReadyNodes                 int
	ReadyState, WorkloadsState fleet.State
	Workloads                  fleet.Workloads
}

func kubeRows(snap *fleet.Snapshot) []kubeRow {
	var out []kubeRow
	for _, n := range snap.Nodegroups {
		out = append(out, kubeRow{n.Name, n.ReadyNodes, n.ReadyState, n.WorkloadsState, n.Workloads})
	}
	return out
}

// The Kubernetes read of a snapshot: the Ready nodes of each nodegroup
// that was described, and the pods on the nodes of one nodegroup. A list
// that fails sets the state of what it covers to Failed, with one failure
// for each nodegroup in the cluster and the region of the snapshot.
func TestFetchKube(t *testing.T) {
	notRead := func(name string) kubeRow { return kubeRow{Name: name} }
	tests := []struct {
		name     string
		need     fleet.KubeNeed
		forbid   fleettest.Operation // the list that is forbidden
		want     []kubeRow
		failures []string
	}{
		{
			name: "ready nodes",
			need: fleet.KubeNeed{Ready: true},
			want: []kubeRow{
				{Name: "ng-a", ReadyNodes: 1, ReadyState: fleet.Collected},
				{Name: "ng-b", ReadyNodes: 1, ReadyState: fleet.Collected},
				notRead("ng-c"),
			},
		},
		{
			// Succeeded pods, pods of other nodes, and pods that are not
			// scheduled do not count.
			name: "the workloads of a nodegroup",
			need: fleet.KubeNeed{Workloads: "ng-a"},
			want: []kubeRow{
				{Name: "ng-a", WorkloadsState: fleet.Collected, Workloads: fleet.Workloads{Pods: 3, SystemPods: 1}},
				notRead("ng-b"), notRead("ng-c"),
			},
		},
		{
			name: "the node list is forbidden", need: fleet.KubeNeed{Ready: true, Workloads: "ng-a"}, forbid: fleettest.KubeListNodes,
			want: []kubeRow{
				{Name: "ng-a", ReadyState: fleet.Failed, WorkloadsState: fleet.Failed},
				{Name: "ng-b", ReadyState: fleet.Failed},
				notRead("ng-c"),
			},
			// One failure for ng-a, though both reads of it failed.
			failures: []string{"Nodegroup ng-a AccessDenied prod us-east-1", "Nodegroup ng-b AccessDenied prod us-east-1"},
		},
		{
			name: "the pod list is forbidden", need: fleet.KubeNeed{Ready: true, Workloads: "ng-b"}, forbid: fleettest.KubeListPods,
			want: []kubeRow{
				{Name: "ng-a", ReadyNodes: 1, ReadyState: fleet.Collected},
				{Name: "ng-b", ReadyNodes: 1, ReadyState: fleet.Collected, WorkloadsState: fleet.Failed},
				notRead("ng-c"),
			},
			failures: []string{"Nodegroup ng-b AccessDenied prod us-east-1"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := kubeWorld()
			if tt.forbid != "" {
				w.SetFault(tt.forbid, fleettest.Fault{Deny: true})
			}
			srv := kubetest.NewServer(w, east, "prod")
			t.Cleanup(srv.Close)
			client, err := kubernetes.NewForConfig(srv.Config())
			if err != nil {
				t.Fatal(err)
			}
			at := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
			snap := kubeSnapshot()
			fetcher := &fleet.Fetcher{Now: func() time.Time { return at }}
			fetcher.FetchKube(testContext(t), client, snap, tt.need)
			if diff := cmp.Diff(tt.want, kubeRows(snap)); diff != "" {
				t.Errorf("nodegroups (-want +got):\n%s", diff)
			}
			var failures []string
			for _, f := range snap.Failures {
				failures = append(failures, fmt.Sprintf("%s %s %s %s %s", f.Kind, f.Name, f.Reason, f.Cluster, f.Region))
			}
			if diff := cmp.Diff(tt.failures, failures); diff != "" {
				t.Errorf("failures (-want +got):\n%s", diff)
			}
			if !snap.KubeAt.Equal(at) {
				t.Errorf("KubeAt = %s, want the time of the fetcher %s", snap.KubeAt, at)
			}
		})
	}
}
