package nodegroup

import (
	"context"
	"fmt"

	"github.com/fatih/color"
	"k8s.io/client-go/kubernetes"

	"github.com/dantech2000/refresh/internal/commands/runner"
	"github.com/dantech2000/refresh/internal/health"
	nodegroupsvc "github.com/dantech2000/refresh/internal/services/nodegroup"
)

// resolveHealthKubeClient builds the Kubernetes client for the pre-flight
// health checks from an optional --kubeconfig path, verifies that it points at
// clusterName (not just whatever kubeconfig context is current), and probes
// connectivity. On any failure it emits an actionable diagnostic and returns
// nil, so the kube-dependent checks degrade to "skipped" rather than running
// against the wrong cluster or failing silently. The returned target lets the
// caller build a metrics client against the same cluster.
func resolveHealthKubeClient(ctx context.Context, api health.ClusterDescriber, region, clusterName, kubeconfig, kubeContext string, humanOutput bool) (kubernetes.Interface, health.KubeSelection) {
	return runner.ResolveClusterKubeClient(ctx, runner.KubeRequest{
		API:         api,
		Cluster:     clusterName,
		Region:      region,
		Kubeconfig:  kubeconfig,
		KubeContext: kubeContext,
		Verbose:     humanOutput,
		SkipNote:    "Workload/PDB checks will be skipped; node readiness falls back to an estimate.",
	})
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
