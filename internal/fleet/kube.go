package fleet

import (
	"context"
	"errors"
	"net"
	"sync"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/dantech2000/refresh/internal/apidoc"
	"github.com/dantech2000/refresh/internal/fanout"
)

// labelNodegroup is the label that EKS sets on the nodes of a managed
// nodegroup.
const labelNodegroup = "eks.amazonaws.com/nodegroup"

// KubeNeed says what FetchKube reads from the Kubernetes API.
type KubeNeed struct {
	// Ready counts the Ready nodes of each managed nodegroup: one node
	// list with the nodegroup label.
	Ready bool
	// Workloads counts the pods on the nodes of the named nodegroup: one
	// pod list per node, with the field selector spec.nodeName, in one
	// wave. A cluster-wide pod list would read every pod of the cluster.
	Workloads string
}

// Workloads is what runs on the nodes of a nodegroup.
type Workloads struct {
	// Pods is the number of pods on the nodes, Succeeded pods left out.
	// SystemPods are the ones in kube-system.
	Pods, SystemPods int
}

// FetchKube fills the Kubernetes groups of snap: ReadyNodes with
// ReadyState on every nodegroup, and Workloads with WorkloadsState on the
// named one. A failed list sets the state Failed and adds a failure for
// each nodegroup that it covers. Nothing is read for a nodegroup that was
// not described.
func (f *Fetcher) FetchKube(ctx context.Context, kube kubernetes.Interface, snap *Snapshot, need KubeNeed) {
	var wg sync.WaitGroup
	var nodes *corev1.NodeList
	var nodesErr error
	if need.Ready || need.Workloads != "" {
		wg.Go(func() {
			nodes, nodesErr = kube.CoreV1().Nodes().List(ctx, metav1.ListOptions{LabelSelector: labelNodegroup})
		})
	}
	wg.Wait()
	snap.KubeAt = f.now()
	if need.Ready {
		f.fillReady(snap, nodes, nodesErr)
	}
	if need.Workloads != "" {
		f.fillWorkloads(ctx, kube, snap, need.Workloads, nodes, nodesErr)
	}
}

func (f *Fetcher) fillReady(snap *Snapshot, nodes *corev1.NodeList, err error) {
	ready := map[string]int{}
	if err == nil {
		for i := range nodes.Items {
			n := &nodes.Items[i]
			if nodeReady(n) {
				ready[n.Labels[labelNodegroup]]++
			}
		}
	}
	for i := range snap.Nodegroups {
		n := &snap.Nodegroups[i]
		if n.State != Collected {
			continue
		}
		if err != nil {
			n.ReadyState = Failed
			snap.Failures = append(snap.Failures, inRegionName(snap, kubeFailure(apidoc.FailureNodegroup, n.Name, "listing nodes", err)))
			continue
		}
		n.ReadyNodes, n.ReadyState = ready[n.Name], Collected
	}
}

func (f *Fetcher) fillWorkloads(ctx context.Context, kube kubernetes.Interface, snap *Snapshot, name string, nodes *corev1.NodeList, nodesErr error) {
	for i := range snap.Nodegroups {
		n := &snap.Nodegroups[i]
		if n.Name != name || n.State != Collected {
			continue
		}
		if nodesErr != nil {
			n.WorkloadsState = Failed
			if !n.hasFailure(snap) {
				snap.Failures = append(snap.Failures, inRegionName(snap, kubeFailure(apidoc.FailureNodegroup, n.Name, "listing nodes", nodesErr)))
			}
			return
		}
		var names []string
		for j := range nodes.Items {
			if nodes.Items[j].Labels[labelNodegroup] == name {
				names = append(names, nodes.Items[j].Name)
			}
		}
		type answer struct {
			pods *corev1.PodList
			err  error
		}
		answers := fanout.ForEach(ctx, names, max(len(names), 1), func(ctx context.Context, node string) answer {
			pods, err := kube.CoreV1().Pods("").List(ctx, metav1.ListOptions{FieldSelector: "spec.nodeName=" + node})
			return answer{pods, err}
		})
		n.WorkloadsState = Collected
		for _, a := range answers {
			if a.err != nil {
				n.WorkloadsState = Failed
				snap.Failures = append(snap.Failures, inRegionName(snap, kubeFailure(apidoc.FailureNodegroup, n.Name, "listing pods", a.err)))
				return
			}
			for j := range a.pods.Items {
				pod := &a.pods.Items[j]
				if pod.Status.Phase == corev1.PodSucceeded {
					continue
				}
				n.Workloads.Pods++
				if pod.Namespace == "kube-system" {
					n.Workloads.SystemPods++
				}
			}
		}
		return
	}
}

// hasFailure reports whether the snapshot already has a failure about n.
func (n *Nodegroup) hasFailure(snap *Snapshot) bool {
	for _, f := range snap.Failures {
		if f.Kind == apidoc.FailureNodegroup && f.Name == n.Name {
			return true
		}
	}
	return false
}

// inRegionName sets the cluster and the region of a failure from the
// snapshot.
func inRegionName(snap *Snapshot, f apidoc.Failure) apidoc.Failure {
	f.Cluster, f.Region = snap.Cluster.Name, snap.Cluster.Region
	return f
}

// nodeReady reports whether the Ready condition of a node is True.
func nodeReady(node *corev1.Node) bool {
	for _, c := range node.Status.Conditions {
		if c.Type == corev1.NodeReady {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}

// kubeFailure returns the failure of a Kubernetes read. The reason follows
// the type of the error: an API status, a context error, or a net error.
// The operation is empty: a Kubernetes read has no IAM action.
func kubeFailure(kind apidoc.FailureKind, name, what string, err error) apidoc.Failure {
	reason := apidoc.ReasonUnknown
	var status apierrors.APIStatus
	var netErr net.Error
	switch {
	case errors.Is(err, context.Canceled):
		reason = apidoc.ReasonInterrupted
	case errors.Is(err, context.DeadlineExceeded):
		reason = apidoc.ReasonTimeout
	case errors.As(err, &status):
		switch {
		case apierrors.IsForbidden(err), apierrors.IsUnauthorized(err):
			reason = apidoc.ReasonAccessDenied
		case apierrors.IsNotFound(err):
			reason = apidoc.ReasonNotFound
		case apierrors.IsTooManyRequests(err):
			reason = apidoc.ReasonThrottled
		case apierrors.IsServerTimeout(err), apierrors.IsInternalError(err), apierrors.IsServiceUnavailable(err):
			reason = apidoc.ReasonServiceError
		case apierrors.IsTimeout(err):
			reason = apidoc.ReasonTimeout
		}
	case errors.As(err, &netErr) && netErr.Timeout():
		// The client's own timeout: the server was reached and did not
		// answer in time.
		reason = apidoc.ReasonTimeout
	case errors.As(err, &netErr):
		reason = apidoc.ReasonNetworkError
	}
	return apidoc.NewFailure(kind, name, reason, what+": "+err.Error())
}

// KubeFailure returns the failure of a Kubernetes read about the item that
// kind and name identify. what names the read, such as "listing nodes".
func KubeFailure(kind apidoc.FailureKind, name, what string, err error) apidoc.Failure {
	return kubeFailure(kind, name, what, err)
}
