package health

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"github.com/dantech2000/refresh/internal/apidoc"
)

// The memory thresholds. Memory is less forgiving than CPU: there is no
// throttling, only eviction, so the bars are tighter. The CPU bars are
// those of the capacity check.
const (
	memSafeHeadroomPercent = 20.0
	memWarnHeadroomPercent = 10.0
)

// Utilization checks the live CPU and memory headroom of the nodes from
// metrics-server: whether the remaining nodes can take the pods of a
// drained one. It is advisory; the capacity check blocks.
func Utilization() Check {
	return Check{Name: "Utilization", Needs: Needs{Kube: true, Metrics: true}, Run: runUtilization}
}

func runUtilization(_ context.Context, in *Input) Result {
	cluster := in.Snapshot.Cluster.Name
	together(func() { _, _ = in.reads.Nodes() }, func() { _, _ = in.reads.NodeMetrics() })
	nodes, err := in.reads.Nodes()
	if err != nil {
		f := kubeFailure(cluster, "listing nodes", err)
		return Result{Outcome: Unreadable, Message: "could not list the nodes: " + f.Error, Failures: []apidoc.Failure{f}}
	}
	metrics, err := in.reads.NodeMetrics()
	if err != nil {
		if apierrors.IsNotFound(err) {
			return Result{Outcome: Skipped, Message: "metrics-server is not installed"}
		}
		f := kubeFailure(cluster, "listing node metrics", err)
		return Result{Outcome: Unreadable, Message: "could not read the node metrics: " + f.Error, Failures: []apidoc.Failure{f}}
	}
	allocCPU := map[string]int64{}
	allocMem := map[string]int64{}
	for i := range nodes.Items {
		n := &nodes.Items[i]
		allocCPU[n.Name] = n.Status.Allocatable.Cpu().MilliValue()
		allocMem[n.Name] = n.Status.Allocatable.Memory().Value()
	}
	var usedCPU, capCPU, usedMem, capMem int64
	counted := 0
	for i := range metrics.Items {
		m := &metrics.Items[i]
		ac, ok := allocCPU[m.Name]
		if !ok || ac == 0 {
			continue
		}
		usedCPU += m.Usage.Cpu().MilliValue()
		capCPU += ac
		usedMem += m.Usage.Memory().Value()
		capMem += allocMem[m.Name]
		counted++
	}
	if counted == 0 || capCPU == 0 || capMem == 0 {
		return Result{Outcome: Skipped, Message: "no node has both allocatable capacity and metrics"}
	}
	return evaluateUtilization(float64(usedCPU)/float64(capCPU)*100, float64(usedMem)/float64(capMem)*100, counted)
}

// evaluateUtilization is the rule, apart from the reads. The worse of CPU
// and memory decides.
func evaluateUtilization(cpuPct, memPct float64, nodes int) Result {
	r := Result{Details: []string{fmt.Sprintf("live use over %s: CPU %.1f%%, memory %.1f%%", plural(nodes, "node"), cpuPct, memPct)}}
	cpuHead, memHead := 100-cpuPct, 100-memPct
	switch {
	case cpuHead < cpuWarnHeadroomPercent || memHead < memWarnHeadroomPercent:
		r.Outcome, r.Message = Fail, fmt.Sprintf("low headroom for a drain (CPU %.0f%%, memory %.0f%% in use)", cpuPct, memPct)
	case cpuHead < cpuSafeHeadroomPercent || memHead < memSafeHeadroomPercent:
		r.Outcome, r.Message = Warn, fmt.Sprintf("limited headroom for a drain (CPU %.0f%%, memory %.0f%% in use)", cpuPct, memPct)
	default:
		r.Outcome, r.Message = Pass, fmt.Sprintf("CPU %.0f%%, memory %.0f%% in use", cpuPct, memPct)
	}
	return r
}
