package health

import (
	"context"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	metricsv1beta1 "k8s.io/metrics/pkg/apis/metrics/v1beta1"
)

// Memory headroom thresholds for absorbing a node drain. Memory is less
// forgiving than CPU (no throttling — eviction/OOM), so the bars are tighter.
const (
	minSafeMemHeadroomPercent = 20.0
	minWarnMemHeadroomPercent = 10.0
)

// NodeMetricsLister is the slice of the metrics.k8s.io client the utilization
// check needs. The metrics clientset's NodeMetricses() satisfies it; tests pass
// a fake.
type NodeMetricsLister interface {
	List(ctx context.Context, opts metav1.ListOptions) (*metricsv1beta1.NodeMetricsList, error)
}

// SetNodeMetrics attaches a metrics-server node-metrics lister, enabling the
// live utilization check. Without it, CheckNodeUtilization is skipped.
func (hc *HealthChecker) SetNodeMetrics(m NodeMetricsLister) { hc.nodeMetrics = m }

// CheckNodeUtilization reports live cluster CPU+memory headroom from
// metrics-server (metrics.k8s.io) — the "can the remaining nodes absorb a
// drain" signal, including memory, which the CloudWatch EC2 path can't see.
// Advisory (non-blocking): the CloudWatch capacity check remains the blocking
// gate. Skips cleanly (not fails) when metrics-server isn't installed.
func (hc *HealthChecker) CheckNodeUtilization(ctx context.Context, _ string) HealthResult {
	if hc.nodeMetrics == nil || hc.k8sClient == nil {
		return HealthResult{
			Name:    "Node Utilization",
			Status:  StatusPass,
			Skipped: true,
			Message: "live utilization unavailable (metrics-server not configured)",
		}
	}

	cpuPct, memPct, nodes, err := hc.nodeUtilization(ctx)
	if err != nil {
		// metrics-server not installed / API not registered → skip, never fail.
		return HealthResult{
			Name:    "Node Utilization",
			Status:  StatusPass,
			Skipped: true,
			Message: fmt.Sprintf("live utilization unavailable: %v", err),
		}
	}
	return evaluateNodeUtilization(cpuPct, memPct, nodes)
}

// nodeUtilization sums live usage (metrics-server) against node allocatable
// (core API) to compute cluster-wide CPU and memory utilization percentages.
// Only nodes present in both sets are counted, so the ratio is comparable.
func (hc *HealthChecker) nodeUtilization(ctx context.Context) (cpuPct, memPct float64, nodes int, err error) {
	nodeList, err := hc.k8sClient.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return 0, 0, 0, fmt.Errorf("listing nodes: %w", err)
	}
	allocCPU := make(map[string]int64, len(nodeList.Items))
	allocMem := make(map[string]int64, len(nodeList.Items))
	for i := range nodeList.Items {
		n := &nodeList.Items[i]
		allocCPU[n.Name] = n.Status.Allocatable.Cpu().MilliValue()
		allocMem[n.Name] = n.Status.Allocatable.Memory().Value()
	}

	metricsList, err := hc.nodeMetrics.List(ctx, metav1.ListOptions{})
	if err != nil {
		return 0, 0, 0, err
	}

	var usedCPU, capCPU, usedMem, capMem int64
	for i := range metricsList.Items {
		m := &metricsList.Items[i]
		ac, ok := allocCPU[m.Name]
		if !ok || ac == 0 {
			continue // node gone or no allocatable — not comparable
		}
		usedCPU += m.Usage.Cpu().MilliValue()
		capCPU += ac
		usedMem += m.Usage.Memory().Value()
		capMem += allocMem[m.Name]
		nodes++
	}
	if nodes == 0 || capCPU == 0 || capMem == 0 {
		return 0, 0, 0, fmt.Errorf("no comparable node metrics available")
	}
	cpuPct = float64(usedCPU) / float64(capCPU) * 100
	memPct = float64(usedMem) / float64(capMem) * 100
	return cpuPct, memPct, nodes, nil
}

// evaluateNodeUtilization turns CPU/memory utilization into an advisory verdict
// (pure, table-testable). Worst of the two dimensions wins.
func evaluateNodeUtilization(cpuPct, memPct float64, nodes int) HealthResult {
	r := HealthResult{Name: "Node Utilization", IsBlocking: false, Status: StatusPass, Score: 100}
	r.Details = append(r.Details, fmt.Sprintf("Live utilization across %d node(s): CPU %.1f%%, memory %.1f%%", nodes, cpuPct, memPct))

	cpuHead, memHead := 100-cpuPct, 100-memPct
	switch {
	case cpuHead < minWarnCPUHeadroomPercent || memHead < minWarnMemHeadroomPercent:
		r.Status = StatusFail
		r.Score = 40
		r.Message = fmt.Sprintf("Low headroom for a drain (CPU %.1f%%, memory %.1f%% in use)", cpuPct, memPct)
	case cpuHead < minSafeCPUHeadroomPercent || memHead < minSafeMemHeadroomPercent:
		r.Status = StatusWarn
		r.Score = 70
		r.Message = fmt.Sprintf("Limited headroom for a drain (CPU %.1f%%, memory %.1f%% in use)", cpuPct, memPct)
	default:
		r.Message = fmt.Sprintf("Healthy headroom (CPU %.1f%%, memory %.1f%% in use)", cpuPct, memPct)
	}
	return r
}
