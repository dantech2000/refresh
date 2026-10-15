package health

import (
	"context"
	"errors"
	"net"
	"sync"
	"sync/atomic"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	metricsv1beta1 "k8s.io/metrics/pkg/apis/metrics/v1beta1"

	"github.com/dantech2000/refresh/internal/apidoc"
	"github.com/dantech2000/refresh/internal/awsx"
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

	// attempted counts the Kubernetes reads that were sent, answered the
	// ones that got an answer from the API, an error answer included, and
	// refused the ones that could not connect at all. A run whose reads
	// all could not connect did not reach the API.
	attempted, answered, refused atomic.Int32
	// connectErr is the error of the first read that could not connect,
	// for the message of the access result.
	connectErr atomic.Pointer[error]
	// unansweredErr is the error of the first read that got no answer,
	// for the same message.
	unansweredErr atomic.Pointer[error]
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
		switch {
		case err == nil || isAPIAnswer(err):
			r.answered.Add(1)
		default:
			r.unansweredErr.CompareAndSwap(nil, &err)
			if connectFailure(err) {
				r.refused.Add(1)
				r.connectErr.CompareAndSwap(nil, &err)
			}
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

// connectFailure reports whether err is a failure to reach the API server
// at all: a connection that could not be opened, because it was refused,
// had no route, or timed out, or because the host does not resolve. A
// private-only endpoint seen from outside its VPC drops the packets, and
// the dial times out: that is no access, as a refused connection is. Any
// other timeout is not one: the client's own timeout fires after the
// connection was made, and the deadline of the run is the user's, not the
// server's.
func connectFailure(err error) bool {
	if errors.Is(err, context.Canceled) || isAPIAnswer(err) {
		return false
	}
	// Before the timeout checks: Go's connect timeout also reports itself
	// as context.DeadlineExceeded.
	if awsx.IsDialFailure(err) {
		return true
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return false
	}
	return true
}

// access returns the result about the Kubernetes access of the run, and
// whether the API could not be reached at all. "Unreachable" is strict:
// the run's context has not ended, no read got an answer, and every read
// that failed could not connect (a dial that timed out included). A
// timeout after the connection was made, a read that failed after another
// answered, or any answer of the server, 5xx included, leaves the checks
// Unreadable with their failures. When no read got an answer, the
// result does not say that the API is reachable: it warns that the API
// did not answer.
func (r *reads) access() (Result, bool) {
	res := Result{Name: AccessCheck}
	attempted, answered, refused := r.attempted.Load(), r.answered.Load(), r.refused.Load()
	switch {
	case r.in.Kube == nil:
		res.Outcome = Warn
		res.Message = "no Kubernetes access: " + r.in.NoKube + "; the Kubernetes checks did not run"
	case r.ctx.Err() == nil && attempted > 0 && answered == 0 && refused == attempted:
		res.Outcome = Warn
		res.Message = "Kubernetes API unreachable: " + (*r.connectErr.Load()).Error() + "; the Kubernetes checks did not run"
		return res, true
	case attempted > 0 && answered == 0:
		res.Outcome = Warn
		res.Message = "Kubernetes API did not answer: " + (*r.unansweredErr.Load()).Error() + "; the Kubernetes checks could not read"
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
