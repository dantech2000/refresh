package cluster

import (
	"context"

	"k8s.io/client-go/kubernetes"

	"github.com/dantech2000/refresh/internal/commands/runner"
	"github.com/dantech2000/refresh/internal/health"
)

// resolveReadinessKubeClient builds the Kubernetes client used by
// `cluster describe --check-readiness` to measure real node readiness, from an
// optional --kubeconfig path. It verifies that the client points at
// clusterName (not just whatever kubeconfig context is current) and probes
// connectivity. On any failure it emits an actionable diagnostic and returns
// nil, so node readiness degrades to honestly "unknown" (desired count only)
// rather than being measured on the wrong cluster or failing the command.
// (REF-130)
func resolveReadinessKubeClient(ctx context.Context, api health.ClusterDescriber, region, clusterName, kubeconfig, kubeContext string, humanOutput bool) (kubernetes.Interface, health.KubeSelection) {
	return runner.ResolveClusterKubeClient(ctx, runner.KubeRequest{
		API:         api,
		Cluster:     clusterName,
		Region:      region,
		Kubeconfig:  kubeconfig,
		KubeContext: kubeContext,
		Verbose:     humanOutput,
		SkipNote:    "Node readiness will show desired capacity only (ready count unknown).",
	})
}
