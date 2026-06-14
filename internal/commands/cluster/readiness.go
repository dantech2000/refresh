package cluster

import (
	"context"

	"github.com/fatih/color"
	"k8s.io/client-go/kubernetes"

	"github.com/dantech2000/refresh/internal/health"
)

// resolveReadinessKubeClient builds the Kubernetes client used by
// `cluster describe --check-readiness` to measure real node readiness, from an
// optional --kubeconfig path, and verifies connectivity. On any failure it
// emits an actionable diagnostic (naming the kubeconfig/context it tried) and
// returns nil, so node readiness degrades to honestly "unknown" (desired count
// only) rather than failing the command. (REF-130)
func resolveReadinessKubeClient(ctx context.Context, kubeconfig string, humanOutput bool) kubernetes.Interface {
	client, diag, err := health.BuildKubeClient(kubeconfig)
	if err != nil {
		if humanOutput {
			color.Yellow("Kubernetes API unavailable: %v (%s)", err, diag)
			color.Yellow("Node readiness will show desired capacity only (ready count unknown).")
		}
		return nil
	}
	if probeErr := health.ProbeConnection(ctx, client); probeErr != nil {
		if humanOutput {
			color.Yellow("Kubernetes API unreachable via %s: %v", diag, probeErr)
			color.Yellow("Node readiness will show desired capacity only (ready count unknown).")
		}
		return nil
	}
	return client
}
