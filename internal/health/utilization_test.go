package health

import (
	"context"
	"errors"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
	metricsv1beta1 "k8s.io/metrics/pkg/apis/metrics/v1beta1"
)

type fakeNodeMetrics struct {
	list *metricsv1beta1.NodeMetricsList
	err  error
}

func (f *fakeNodeMetrics) List(_ context.Context, _ metav1.ListOptions) (*metricsv1beta1.NodeMetricsList, error) {
	return f.list, f.err
}

func allocNode(name, cpu, mem string) *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Status: corev1.NodeStatus{Allocatable: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse(cpu),
			corev1.ResourceMemory: resource.MustParse(mem),
		}},
	}
}

func nodeUsage(name, cpu, mem string) metricsv1beta1.NodeMetrics {
	return metricsv1beta1.NodeMetrics{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Usage: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse(cpu),
			corev1.ResourceMemory: resource.MustParse(mem),
		},
	}
}

func TestEvaluateNodeUtilization(t *testing.T) {
	tests := []struct {
		name       string
		cpu, mem   float64
		wantStatus HealthStatus
	}{
		{"healthy", 25, 25, StatusPass},
		{"cpu limited warns", 75, 25, StatusWarn}, // cpu headroom 25 < 30 safe
		{"mem limited warns", 25, 85, StatusWarn}, // mem headroom 15, between 10 and 20
		{"cpu low fails", 90, 25, StatusFail},     // cpu headroom 10 < 15
		{"mem low fails", 25, 95, StatusFail},     // mem headroom 5 < 10
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := evaluateNodeUtilization(tc.cpu, tc.mem, 3)
			if r.Status != tc.wantStatus {
				t.Errorf("status = %s, want %s (%+v)", r.Status, tc.wantStatus, r)
			}
			if r.IsBlocking {
				t.Error("node utilization should be advisory (non-blocking)")
			}
		})
	}
}

func TestCheckNodeUtilization_Live(t *testing.T) {
	// One node: 2 CPU / 8Gi allocatable; using 500m / 2Gi → 25% / 25%.
	kube := fake.NewClientset(allocNode("ip-1", "2", "8Gi"))
	metrics := &fakeNodeMetrics{list: &metricsv1beta1.NodeMetricsList{Items: []metricsv1beta1.NodeMetrics{
		nodeUsage("ip-1", "500m", "2Gi"),
		nodeUsage("ip-gone", "1", "4Gi"), // not in kube nodes → ignored
	}}}

	hc := &HealthChecker{k8sClient: kube, nodeMetrics: metrics}
	r := hc.CheckNodeUtilization(context.Background(), "prod")
	if r.Skipped || r.Status != StatusPass {
		t.Fatalf("expected healthy non-skipped, got %+v", r)
	}
	if len(r.Details) == 0 || !strings.Contains(r.Details[0], "1 node") {
		t.Errorf("details should count only the comparable node: %v", r.Details)
	}
	if !strings.Contains(r.Details[0], "CPU 25.0%") || !strings.Contains(r.Details[0], "memory 25.0%") {
		t.Errorf("utilization math wrong: %v", r.Details)
	}
}

func TestCheckNodeUtilization_SkipNoMetricsClient(t *testing.T) {
	hc := &HealthChecker{k8sClient: fake.NewClientset()}
	r := hc.CheckNodeUtilization(context.Background(), "prod")
	if !r.Skipped {
		t.Errorf("no metrics client should skip, got %+v", r)
	}
}

func TestCheckNodeUtilization_SkipWhenMetricsServerAbsent(t *testing.T) {
	kube := fake.NewClientset(allocNode("ip-1", "2", "8Gi"))
	// metrics-server not installed → List errors; must skip, not fail.
	hc := &HealthChecker{k8sClient: kube, nodeMetrics: &fakeNodeMetrics{err: errors.New("the server could not find the requested resource (get nodes.metrics.k8s.io)")}}
	r := hc.CheckNodeUtilization(context.Background(), "prod")
	if !r.Skipped || r.Status == StatusFail {
		t.Errorf("absent metrics-server should skip (not fail), got %+v", r)
	}
}
