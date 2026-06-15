package nodegroup

import (
	"context"
	"fmt"

	"github.com/fatih/color"
	"k8s.io/client-go/kubernetes"

	"github.com/dantech2000/refresh/internal/health"
	nodegroupsvc "github.com/dantech2000/refresh/internal/services/nodegroup"
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

// warnInstanceTypeAvailability runs the EC2 instance-type-availability pre-flight
// and prints a non-blocking warning for any (type, AZ) the nodegroup spans where
// the type isn't offered — nodes would fail to launch there. Best-effort: any
// error is swallowed so it never blocks a scale or roll. (REF-143)
func warnInstanceTypeAvailability(ctx context.Context, svc *nodegroupsvc.ServiceImpl, clusterName, nodegroupName string) {
	unavailable, err := svc.CheckInstanceTypeAvailability(ctx, clusterName, nodegroupName)
	if err != nil || len(unavailable) == 0 {
		return
	}
	color.Yellow("Pre-flight: instance type(s) not offered in some of the nodegroup's AZs — new nodes may fail to launch there:")
	for _, u := range unavailable {
		fmt.Printf("  - %s not offered in %s\n", u.InstanceType, u.AvailabilityZone)
	}
	color.Yellow("  Note: this checks availability, not live capacity (only a launch reveals InsufficientInstanceCapacity).")
}
