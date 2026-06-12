package nodegroup

import (
	"context"

	"github.com/fatih/color"
	"k8s.io/client-go/kubernetes"

	"github.com/dantech2000/refresh/internal/health"
)

// resolveHealthKubeClient builds the Kubernetes client for the pre-flight
// health checks from an optional --kubeconfig path and verifies connectivity.
// On any failure it emits an actionable diagnostic (naming the kubeconfig /
// context it tried) and returns nil, so the kube-dependent checks degrade to
// "skipped" rather than failing silently.
func resolveHealthKubeClient(ctx context.Context, kubeconfig string, humanOutput bool) kubernetes.Interface {
	client, diag, err := health.BuildKubeClient(kubeconfig)
	if err != nil {
		if humanOutput {
			color.Yellow("Kubernetes checks unavailable: %v (%s)", err, diag)
			color.Yellow("Workload/PDB checks will be skipped; node readiness falls back to an estimate.")
		}
		return nil
	}
	if probeErr := health.ProbeConnection(ctx, client); probeErr != nil {
		if humanOutput {
			color.Yellow("Kubernetes API unreachable via %s: %v", diag, probeErr)
			color.Yellow("Workload/PDB checks will be skipped; node readiness falls back to an estimate.")
		}
		return nil
	}
	return client
}
