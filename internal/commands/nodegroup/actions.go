package nodegroup

import (
	"context"
	"fmt"
	"io"

	"k8s.io/client-go/kubernetes"

	"github.com/dantech2000/refresh/internal/commands/runner"
	"github.com/dantech2000/refresh/internal/health"
	"github.com/dantech2000/refresh/internal/render"
	nodegroupsvc "github.com/dantech2000/refresh/internal/services/nodegroup"
	"github.com/dantech2000/refresh/internal/ui"
)

// resolveHealthKubeClient builds the Kubernetes client for the pre-flight
// health checks from an optional --kubeconfig path, verifies that it points at
// clusterName (not just whatever kubeconfig context is current), and probes
// connectivity. On any failure it returns nil (a cluster mismatch is always
// reported on stderr, other failures only when humanOutput is set), so the
// kube-dependent checks degrade to "skipped" rather than running against the
// wrong cluster. The returned KubeSelection lets the caller build a metrics
// client (health.BuildMetricsClient) against the same cluster.
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
// error is swallowed so it never blocks a scale or roll. The warning goes to
// stderr. (REF-143)
func warnInstanceTypeAvailability(ctx context.Context, svc *nodegroupsvc.ServiceImpl, clusterName, nodegroupName string) {
	warnInstanceTypeAvailabilityTo(ctx, ui.Stderr, svc, clusterName, nodegroupName)
}

// warnInstanceTypeAvailabilityTo is warnInstanceTypeAvailability writing to w.
func warnInstanceTypeAvailabilityTo(ctx context.Context, w io.Writer, svc *nodegroupsvc.ServiceImpl, clusterName, nodegroupName string) {
	unavailable, err := svc.CheckInstanceTypeAvailability(ctx, clusterName, nodegroupName)
	if err != nil || len(unavailable) == 0 {
		return
	}
	th := render.Default(w)
	_, _ = fmt.Fprintln(w, th.Line(render.Warn, "Pre-flight: instance type(s) not offered in some of the nodegroup's AZs — new nodes may fail to launch there:"))
	for _, u := range unavailable {
		_, _ = fmt.Fprintf(w, "  - %s not offered in %s\n", u.InstanceType, u.AvailabilityZone)
	}
	_, _ = fmt.Fprintln(w, th.Paint(th.Pal.Dim, "  Note: this checks availability, not live capacity (only a launch reveals InsufficientInstanceCapacity)."))
}
