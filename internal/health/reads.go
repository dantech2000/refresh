package health

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	metricsv1beta1 "k8s.io/metrics/pkg/apis/metrics/v1beta1"

	"github.com/dantech2000/refresh/internal/apidoc"
	"github.com/dantech2000/refresh/internal/fleet"
)

// reads holds the Kubernetes lists of one run, each made once. Every
// caller gets the same answer or the same error: a list that failed is not
// tried again within the run, so two checks that share a read cannot
// disagree. sync.OnceValues keeps the error, which is why the memo of the
// fleet package is not used here.
//
// Every read runs under the context of the run: when it ends, the read
// that is in flight fails with its error, and every check sees the same
// failure.
type reads struct {
	ctx context.Context //nolint:containedctx // the context of the run, for reads that start inside a check
	in  *Input

	nodes       func() (*corev1.NodeList, error)
	pdbs        func() (*policyv1.PodDisruptionBudgetList, error)
	deployments func() (*appsv1.DeploymentList, error)
	nodeMetrics func() (*metricsv1beta1.NodeMetricsList, error)

	mu   sync.Mutex
	pods map[string]func() (*corev1.PodList, error)

	// attempted and answered count the Kubernetes reads that were sent
	// and those that got an answer from the API, an error answer included.
	// A run with reads and no answer could not connect.
	attempted, answered atomic.Int32
	// connectErr is the error of the first read that did not connect, for
	// the message of the access result.
	connectErr atomic.Pointer[error]
}

func newReads(ctx context.Context, in *Input) *reads {
	r := &reads{ctx: ctx, in: in, pods: map[string]func() (*corev1.PodList, error){}}
	r.nodes = once(r, func() (*corev1.NodeList, error) {
		return in.Kube.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	})
	r.pdbs = once(r, func() (*policyv1.PodDisruptionBudgetList, error) {
		return in.Kube.PolicyV1().PodDisruptionBudgets("").List(ctx, metav1.ListOptions{})
	})
	r.deployments = once(r, func() (*appsv1.DeploymentList, error) {
		return in.Kube.AppsV1().Deployments("").List(ctx, metav1.ListOptions{})
	})
	r.nodeMetrics = once(r, func() (*metricsv1beta1.NodeMetricsList, error) {
		return in.Metrics.List(ctx, metav1.ListOptions{})
	})
	return r
}

// Nodes lists every node of the cluster, once per run.
func (r *reads) Nodes() (*corev1.NodeList, error) { return r.nodes() }

// PDBs lists every PodDisruptionBudget of the cluster, once per run.
func (r *reads) PDBs() (*policyv1.PodDisruptionBudgetList, error) { return r.pdbs() }

// Deployments lists every deployment of the cluster, once per run.
func (r *reads) Deployments() (*appsv1.DeploymentList, error) { return r.deployments() }

// NodeMetrics lists the node metrics of metrics-server, once per run.
func (r *reads) NodeMetrics() (*metricsv1beta1.NodeMetricsList, error) { return r.nodeMetrics() }

// Pods lists the pods of one namespace, once per namespace and run.
func (r *reads) Pods(namespace string) (*corev1.PodList, error) {
	r.mu.Lock()
	list, ok := r.pods[namespace]
	if !ok {
		list = once(r, func() (*corev1.PodList, error) {
			return r.in.Kube.CoreV1().Pods(namespace).List(r.ctx, metav1.ListOptions{})
		})
		r.pods[namespace] = list
	}
	r.mu.Unlock()
	return list()
}

// together starts the reads at once and waits for them, so that a check
// that needs several makes them in one round trip.
func together(reads ...func()) {
	var wg sync.WaitGroup
	for _, read := range reads {
		wg.Go(read)
	}
	wg.Wait()
}

// once returns a function that makes the read on its first call, counts
// it for the access result, and returns the same answer to every call.
func once[T any](r *reads, read func() (T, error)) func() (T, error) {
	return sync.OnceValues(func() (T, error) {
		v, err := read()
		r.attempted.Add(1)
		if err == nil || isAPIAnswer(err) {
			r.answered.Add(1)
		} else {
			r.connectErr.CompareAndSwap(nil, &err)
		}
		return v, err
	})
}

// isAPIAnswer reports whether err is an answer of the API server, such as
// 403, and not a failure to reach it.
func isAPIAnswer(err error) bool {
	var status apierrors.APIStatus
	return errors.As(err, &status)
}

// access returns the result about the Kubernetes access of the run, and
// whether the API could not be reached at all.
func (r *reads) access() (Result, bool) {
	res := Result{Name: AccessCheck}
	switch {
	case r.in.Kube == nil:
		res.Outcome = Warn
		res.Message = "no Kubernetes access: " + r.in.NoKube + "; the Kubernetes checks did not run"
	case r.attempted.Load() > 0 && r.answered.Load() == 0:
		res.Outcome = Warn
		res.Message = "Kubernetes API unreachable: " + (*r.connectErr.Load()).Error() + "; the Kubernetes checks did not run"
		return res, true
	default:
		res.Outcome = Pass
		res.Message = "Kubernetes API reachable"
	}
	return res, false
}

// kubeFailure returns the failure of a Kubernetes read about the cluster.
func kubeFailure(cluster, what string, err error) apidoc.Failure {
	return fleet.KubeFailure(apidoc.FailureCluster, cluster, what, err)
}
