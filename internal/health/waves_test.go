package health_test

import (
	"testing"

	"github.com/dantech2000/refresh/internal/awsx/awsxtest"
	"github.com/dantech2000/refresh/internal/fleet/fleettest"
	"github.com/dantech2000/refresh/internal/health"
	"github.com/dantech2000/refresh/internal/kube/kubetest"
)

// The gate is one AWS round trip and two Kubernetes round trips after the
// snapshot: the metric calls and the quota reads, the vCPUs of the target's
// instance types among them, the lists that depend on nothing, then the
// pods of the namespaces that a budget or a second budget sends the check
// to. The two gates are independent, so the AWS wave and the Kubernetes
// waves may interleave in any order.
func TestGate_Waves(t *testing.T) {
	h := newHarness(t, tight("web"))
	ctx := testContext(t)
	in := h.input(ctx, "ng-a")
	aws := awsxtest.NewGate(t, h.aws.Handler)
	k8s := kubetest.NewGate(t, h.kube.Handler)
	done := make(chan []health.Result, 1)
	go func() { done <- health.Run(ctx, health.Preflight(), in) }()
	aws.Wave(fleettest.CloudWatchGetMetricData, fleettest.CloudWatchGetMetricData, fleettest.CloudWatchGetMetricData, fleettest.ServiceQuotasGetServiceQuota,
		fleettest.EC2DescribeInstanceTypes)
	k8s.Wave(fleettest.KubeListNodes, fleettest.KubeListPDBs, fleettest.KubeListDeployments, fleettest.KubeListNodeMetrics,
		fleettest.KubeListPods, fleettest.KubeListPods, fleettest.KubeListPods)
	k8s.Wave(fleettest.KubeListPods) // default, where the tight budget is
	aws.Done()
	k8s.Done()
	results := <-done
	if v := health.Gate(results, health.Policy{}); v.Decision != health.Block {
		t.Errorf("verdict = %+v, want Block on the tight budget", v)
	}
}
