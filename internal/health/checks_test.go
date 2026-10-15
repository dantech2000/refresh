package health_test

import (
	"context"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws/retry"
	"github.com/google/go-cmp/cmp"
	"k8s.io/client-go/kubernetes"
	metricsclient "k8s.io/metrics/pkg/client/clientset/versioned"
	metricsv1beta1 "k8s.io/metrics/pkg/client/clientset/versioned/typed/metrics/v1beta1"

	"github.com/dantech2000/refresh/internal/apidoc"
	"github.com/dantech2000/refresh/internal/awsx"
	"github.com/dantech2000/refresh/internal/awsx/awsxtest"
	"github.com/dantech2000/refresh/internal/fleet"
	"github.com/dantech2000/refresh/internal/fleet/fleettest"
	"github.com/dantech2000/refresh/internal/health"
	"github.com/dantech2000/refresh/internal/kube/kubetest"
)

const east = "us-east-1"

// now is the time of every run: long after the world's clock, so a node
// of the seed is settled and not joining.
var now = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

// harness is a fake AWS and a fake Kubernetes API over one world, with
// the cluster prod: two nodegroups of two nodes, system pods, two web
// pods on ng-a and two api pods on ng-b, a budget for each with room for
// a disruption, deployments for both, and metrics that pass every check.
type harness struct {
	t      *testing.T
	world  *fleettest.World
	aws    *awsxtest.Server
	kube   *kubetest.Server
	client kubernetes.Interface
	// metrics is the metrics client; nil when a test wants none.
	metrics  metricsv1beta1.NodeMetricsInterface
	provider *awsx.Provider
}

// edit changes the seed before the servers start.
type edit func(s *fleettest.State, c *fleettest.Cluster)

func newHarness(t *testing.T, edits ...edit) *harness {
	t.Helper()
	world := fleettest.New()
	world.Edit(func(s *fleettest.State) {
		c := &fleettest.Cluster{
			Name: "prod", Region: east, Version: "1.33",
			Nodegroups: []*fleettest.Nodegroup{
				{Name: "ng-a", Version: "1.33", AutoScalingGroups: []string{"asg-a"}, Desired: 2},
				{Name: "ng-b", Version: "1.33", AutoScalingGroups: []string{"asg-b"}, Desired: 2},
			},
			Nodes: []*fleettest.Node{
				{Name: "a-1", Nodegroup: "ng-a", UsedMilliCPU: 400, UsedMemoryMi: 2048},
				{Name: "a-2", Nodegroup: "ng-a", UsedMilliCPU: 400, UsedMemoryMi: 2048},
				{Name: "b-1", Nodegroup: "ng-b", UsedMilliCPU: 400, UsedMemoryMi: 2048},
				{Name: "b-2", Nodegroup: "ng-b", UsedMilliCPU: 400, UsedMemoryMi: 2048},
			},
			Pods: []*fleettest.Pod{
				{Namespace: "kube-system", Name: "coredns-1", Node: "a-1", Labels: map[string]string{"k8s-app": "kube-dns"}},
				{Namespace: "kube-system", Name: "coredns-2", Node: "b-1", Labels: map[string]string{"k8s-app": "kube-dns"}},
				{Namespace: "kube-system", Name: "kube-proxy-1", Node: "a-1"},
				{Namespace: "default", Name: "web-1", Node: "a-1", Labels: map[string]string{"app": "web"}},
				{Namespace: "default", Name: "web-2", Node: "a-2", Labels: map[string]string{"app": "web"}},
				{Namespace: "default", Name: "api-1", Node: "b-1", Labels: map[string]string{"app": "api"}},
				{Namespace: "default", Name: "api-2", Node: "b-2", Labels: map[string]string{"app": "api"}},
			},
			PDBs: []fleettest.PDB{
				{Namespace: "default", Name: "web", Selector: map[string]string{"app": "web"}, MinAvailable: 1, DisruptionsAllowed: 1, CurrentHealthy: 2, DesiredHealthy: 1, ExpectedPods: 2},
				{Namespace: "default", Name: "api", Selector: map[string]string{"app": "api"}, MinAvailable: 1, DisruptionsAllowed: 1, CurrentHealthy: 2, DesiredHealthy: 1, ExpectedPods: 2},
			},
			Deployments: []fleettest.Deployment{
				{Namespace: "default", Name: "web", Replicas: 2, Selector: map[string]string{"app": "web"}},
				{Namespace: "default", Name: "api", Replicas: 2, Selector: map[string]string{"app": "api"}},
				{Namespace: "kube-system", Name: "coredns", Replicas: 2, Selector: map[string]string{"k8s-app": "kube-dns"}},
			},
		}
		s.Clusters = []*fleettest.Cluster{c}
		s.AutoScalingGroups = []fleettest.AutoScalingGroup{
			{Name: "asg-a", InstanceIDs: []string{"i-a-1", "i-a-2"}},
			{Name: "asg-b", InstanceIDs: []string{"i-b-1", "i-b-2"}},
		}
		for _, id := range []string{"i-a-1", "i-a-2", "i-b-1", "i-b-2"} {
			s.Instances = append(s.Instances, fleettest.Instance{ID: id, ImageID: "ami-1"})
			s.Metrics = append(s.Metrics, cpuMetric(id, 30, 30))
		}
		s.Metrics = append(s.Metrics, eksMetrics("prod", 1<<30, 48210, 5, 0)...)
		s.Metrics = append(s.Metrics, usageMetric(40))
		s.Quotas = []fleettest.Quota{{Region: east, ServiceCode: "ec2", QuotaCode: "L-1216C47A", Value: 128}}
		for _, e := range edits {
			e(s, c)
		}
	})
	h := &harness{t: t, world: world}
	h.aws = awsxtest.NewServer(world)
	t.Cleanup(h.aws.Close)
	h.provider = awsx.NewProvider(h.aws.Config(east), func(o *awsx.ProviderOptions) {
		o.Backoff = retry.BackoffDelayerFunc(func(int, error) (time.Duration, error) { return 0, nil })
	})
	h.kube = kubetest.NewServer(world, east, "prod")
	t.Cleanup(h.kube.Close)
	var err error
	if h.client, err = kubernetes.NewForConfig(h.kube.Config()); err != nil {
		t.Fatal(err)
	}
	mc, err := metricsclient.NewForConfig(h.kube.Config())
	if err != nil {
		t.Fatal(err)
	}
	h.metrics = mc.MetricsV1beta1().NodeMetricses()
	return h
}

func cpuMetric(id string, values ...float64) fleettest.Metric {
	return fleettest.Metric{Region: east, Namespace: "AWS/EC2", Name: "CPUUtilization", Dimensions: map[string]string{"InstanceId": id}, Values: values}
}

func eksMetrics(cluster string, etcd, requests, errors5xx, throttled float64) []fleettest.Metric {
	var out []fleettest.Metric
	for name, value := range map[string]float64{
		"etcd_mvcc_db_total_size_in_use_in_bytes": etcd, "apiserver_request_total": requests,
		"apiserver_request_total_5XX": errors5xx, "apiserver_request_total_429": throttled,
	} {
		out = append(out, fleettest.Metric{Region: east, Namespace: "AWS/EKS", Name: name, Dimensions: map[string]string{"ClusterName": cluster}, Values: []float64{value}})
	}
	return out
}

func usageMetric(values ...float64) fleettest.Metric {
	return fleettest.Metric{
		Region: east, Namespace: "AWS/Usage", Name: "ResourceCount", Values: values,
		Dimensions: map[string]string{"Service": "EC2", "Type": "Resource", "Resource": "vCPU", "Class": "Standard/OnDemand"},
	}
}

func testContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// input reads the snapshot and returns the input of a run.
func (h *harness) input(ctx context.Context, targets ...string) *health.Input {
	h.t.Helper()
	var fetcher fleet.Fetcher
	fetcher.Now = func() time.Time { return now }
	snap, err := fetcher.Fetch(ctx, h.provider.Clients(east), "prod", fleet.Need{Nodegroups: true, InstanceIDs: true})
	if err != nil {
		h.t.Fatal(err)
	}
	return &health.Input{
		Snapshot: snap, Targets: targets, Clients: h.provider.Clients(east),
		Kube: h.client, NoKube: "no kubeconfig context points at prod", Metrics: h.metrics, Now: now,
	}
}

// run runs one check and returns its result.
func (h *harness) run(ctx context.Context, check health.Check, targets ...string) health.Result {
	h.t.Helper()
	return health.Run(ctx, []health.Check{check}, h.input(ctx, targets...))[0]
}

// want is what a test compares of a result.
type want struct {
	outcome  health.Outcome
	message  string // a part of the message
	findings []string
	failures int
}

func check(t *testing.T, got health.Result, w want) {
	t.Helper()
	if got.Outcome != w.outcome {
		t.Errorf("outcome = %s (%s), want %s", got.Outcome, got.Message, w.outcome)
	}
	if !strings.Contains(got.Message, w.message) {
		t.Errorf("message = %q, want it to contain %q", got.Message, w.message)
	}
	if w.findings != nil {
		if diff := cmp.Diff(w.findings, got.Findings); diff != "" {
			t.Errorf("findings (-want +got):\n%s", diff)
		}
	}
	if len(got.Failures) != w.failures {
		t.Errorf("failures = %+v, want %d", got.Failures, w.failures)
	}
	for _, f := range got.Failures {
		if f.Cluster == "" && f.Kind != apidoc.FailureCluster || f.Region != east {
			t.Errorf("failure without a cluster or a region: %+v", f)
		}
	}
}

func nodeNotReady(names ...string) edit {
	return func(_ *fleettest.State, c *fleettest.Cluster) {
		for _, n := range c.Nodes {
			if slices.Contains(names, n.Name) {
				n.NotReady = true
			}
		}
	}
}

func nodegroupStatus(name string, status fleettest.ResourceStatus) edit {
	return func(_ *fleettest.State, c *fleettest.Cluster) { c.Nodegroup(name).Status = status }
}

func TestNodes(t *testing.T) {
	joining := func(_ *fleettest.State, c *fleettest.Cluster) {
		c.Nodes = append(c.Nodes, &fleettest.Node{Name: "a-3", Nodegroup: "ng-a", NotReady: true, CreatedAt: now.Add(-time.Minute)})
	}
	tests := []struct {
		name    string
		edits   []edit
		noKube  bool
		targets []string
		want    want
	}{
		{name: "every node ready", want: want{outcome: health.Pass, message: "4/4 nodes ready"}},
		{name: "one node not ready", edits: []edit{nodeNotReady("b-1")}, want: want{outcome: health.Warn, message: "3/4 nodes ready", findings: []string{"b-1"}}},
		{name: "below half ready", edits: []edit{nodeNotReady("a-1", "a-2", "b-1")}, want: want{outcome: health.Fail, message: "below the 50% minimum", findings: []string{"a-1", "a-2", "b-1"}}},
		{name: "no node ready", edits: []edit{nodeNotReady("a-1", "a-2", "b-1", "b-2")}, want: want{outcome: health.Fail, message: "no ready nodes"}},
		{name: "a joining node", edits: []edit{joining}, want: want{outcome: health.Warn, message: "4/5 nodes ready", findings: []string{"a-3"}}},
		{
			name: "a node whose kubelet stopped is not joining",
			edits: []edit{func(_ *fleettest.State, c *fleettest.Cluster) {
				c.Nodes = append(c.Nodes, &fleettest.Node{Name: "a-3", Nodegroup: "ng-a", NotReady: true, ReadyReason: "NodeStatusUnknown", CreatedAt: now.Add(-time.Minute)})
				for _, n := range c.Nodes[:4] {
					n.NotReady = true
				}
			}},
			want: want{outcome: health.Fail, message: "no ready nodes"},
		},
		{name: "no node at all", edits: []edit{func(_ *fleettest.State, c *fleettest.Cluster) { c.Nodes = nil }}, want: want{outcome: health.Fail, message: "no nodes found"}},
		{name: "no node yet while a nodegroup is creating", edits: []edit{nodegroupStatus("ng-a", fleettest.StatusCreating), func(_ *fleettest.State, c *fleettest.Cluster) { c.Nodes = nil }}, targets: []string{"ng-b"}, want: want{outcome: health.Warn, message: "no nodes yet; nodegroups still scaling: ng-a (CREATING)"}},
		{name: "no node and a degraded nodegroup while another is creating", edits: []edit{nodegroupStatus("ng-a", fleettest.StatusCreating), nodegroupStatus("ng-b", fleettest.StatusDegraded), func(_ *fleettest.State, c *fleettest.Cluster) { c.Nodes = nil }}, want: want{outcome: health.Fail, message: "no nodes found"}},
		{name: "a degraded neighbour", edits: []edit{nodegroupStatus("ng-b", fleettest.StatusDegraded)}, targets: []string{"ng-a"}, want: want{outcome: health.Warn, message: "issues: ng-b (DEGRADED)", findings: []string{"ng-b (DEGRADED)"}}},
		{name: "a degraded target", edits: []edit{nodegroupStatus("ng-a", fleettest.StatusDegraded)}, targets: []string{"ng-a"}, want: want{outcome: health.Fail, message: "a target nodegroup is not ACTIVE", findings: []string{"ng-a (DEGRADED)"}}},
		{name: "an updating target", edits: []edit{nodegroupStatus("ng-a", fleettest.StatusUpdating)}, targets: []string{"ng-a"}, want: want{outcome: health.Fail, message: "a target nodegroup is not ACTIVE"}},
		{name: "an updating neighbour", edits: []edit{nodegroupStatus("ng-b", fleettest.StatusUpdating)}, targets: []string{"ng-a"}, want: want{outcome: health.Pass, message: "4/4 nodes ready"}},
		{name: "estimated", noKube: true, want: want{outcome: health.Pass, message: "4/4 nodes ready (estimated)"}},
		{name: "estimated with a degraded nodegroup", noKube: true, edits: []edit{nodegroupStatus("ng-b", fleettest.StatusDegraded)}, want: want{outcome: health.Warn, message: "2/4 nodes ready (estimated)", findings: []string{"ng-b (DEGRADED)"}}},
		{name: "estimated with every nodegroup degraded", noKube: true, edits: []edit{nodegroupStatus("ng-a", fleettest.StatusDegraded), nodegroupStatus("ng-b", fleettest.StatusDegraded)}, want: want{outcome: health.Fail, message: "no ready nodes (estimated)"}},
		{name: "estimated with no ready nodes while a nodegroup is creating", noKube: true, edits: []edit{nodegroupStatus("ng-a", fleettest.StatusCreating), nodegroupStatus("ng-b", fleettest.StatusDegraded)}, want: want{outcome: health.Warn, message: "no ready nodes yet (estimated); issues: ng-b (DEGRADED)"}},
		{name: "estimated with every nodegroup mid-change", noKube: true, edits: []edit{nodegroupStatus("ng-a", fleettest.StatusUpdating), nodegroupStatus("ng-b", fleettest.StatusCreating)}, want: want{outcome: health.Warn, message: "every nodegroup is mid-change"}},
		{name: "estimated with no managed capacity", noKube: true, edits: []edit{func(_ *fleettest.State, c *fleettest.Cluster) { c.Nodegroups = nil }}, want: want{outcome: health.Skipped, message: "no managed nodegroup capacity"}},
		{name: "estimated with a degraded target", noKube: true, edits: []edit{nodegroupStatus("ng-a", fleettest.StatusDegraded)}, targets: []string{"ng-a"}, want: want{outcome: health.Fail, message: "a target nodegroup is not ACTIVE"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t, tt.edits...)
			ctx := testContext(t)
			in := h.input(ctx, tt.targets...)
			if tt.noKube {
				in.Kube, in.Metrics = nil, nil
			}
			check(t, health.Run(ctx, []health.Check{health.Nodes()}, in)[0], tt.want)
		})
	}
}

func TestNodes_Unreadable(t *testing.T) {
	h := newHarness(t)
	h.world.SetFault(fleettest.KubeListNodes, fleettest.Fault{Deny: true})
	check(t, h.run(testContext(t), health.Nodes()), want{outcome: health.Unreadable, message: "could not list the nodes", failures: 1})

	h = newHarness(t)
	h.world.SetScopedFault(fleettest.Scope{Operation: fleettest.EKSDescribeNodegroup, Nodegroup: "ng-b"}, fleettest.Fault{Deny: true})
	check(t, h.run(testContext(t), health.Nodes()), want{outcome: health.Unreadable, message: "the nodegroups could not all be read"})
}

func TestCapacity(t *testing.T) {
	cpu := func(values ...float64) edit {
		return func(s *fleettest.State, _ *fleettest.Cluster) {
			s.Metrics = slices.DeleteFunc(s.Metrics, func(m fleettest.Metric) bool { return m.Name == "CPUUtilization" })
			for i, id := range []string{"i-a-1", "i-a-2", "i-b-1", "i-b-2"} {
				if i < len(values) {
					s.Metrics = append(s.Metrics, cpuMetric(id, values[i]))
				}
			}
		}
	}
	tests := []struct {
		name  string
		edits []edit
		want  want
	}{
		{name: "headroom", want: want{outcome: health.Pass, message: "CPU mean 30%, peak 30%, 4 instances"}},
		{name: "limited headroom", edits: []edit{cpu(80, 80, 80, 80)}, want: want{outcome: health.Warn, message: "limited CPU headroom (mean 80%"}},
		{name: "no headroom", edits: []edit{cpu(90, 90, 90, 90)}, want: want{outcome: health.Fail, message: "insufficient CPU headroom"}},
		{name: "a saturated instance", edits: []edit{cpu(10, 10, 10, 96)}, want: want{outcome: health.Fail, message: "near CPU saturation (peak 96%"}},
		{name: "a busy instance", edits: []edit{cpu(10, 10, 10, 88)}, want: want{outcome: health.Warn, message: "uneven CPU load (peak 88%"}},
		{name: "no datapoints", edits: []edit{cpu()}, want: want{outcome: health.Warn, message: "no CPU metrics reported for the 4 instances"}},
		{name: "some datapoints", edits: []edit{cpu(20, 40)}, want: want{outcome: health.Pass, message: "mean 30%, peak 40%, 2 instances"}},
		{name: "no instances", edits: []edit{func(s *fleettest.State, _ *fleettest.Cluster) { s.AutoScalingGroups = nil }}, want: want{outcome: health.Skipped, message: "no managed nodegroup instances"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t, tt.edits...)
			check(t, h.run(testContext(t), health.Capacity()), tt.want)
		})
	}
	t.Run("unreadable", func(t *testing.T) {
		h := newHarness(t)
		h.world.SetFault(fleettest.CloudWatchGetMetricData, fleettest.Fault{Deny: true})
		check(t, h.run(testContext(t), health.Capacity()), want{outcome: health.Unreadable, message: "cloudwatch:GetMetricData", failures: 1})
		h = newHarness(t)
		h.world.SetFault(fleettest.AutoScalingDescribeAutoScalingGroups, fleettest.Fault{Deny: true})
		check(t, h.run(testContext(t), health.Capacity()), want{outcome: health.Unreadable, message: "the instances of 2 nodegroups could not be read"})
	})
}

func TestControlPlane(t *testing.T) {
	metrics := func(etcd, requests, errors5xx, throttled float64) edit {
		return func(s *fleettest.State, _ *fleettest.Cluster) {
			s.Metrics = slices.DeleteFunc(s.Metrics, func(m fleettest.Metric) bool { return m.Namespace == "AWS/EKS" })
			s.Metrics = append(s.Metrics, eksMetrics("prod", etcd, requests, errors5xx, throttled)...)
		}
	}
	tests := []struct {
		name  string
		edits []edit
		want  want
	}{
		{name: "healthy", want: want{outcome: health.Pass, message: "etcd 12% of 8 GiB"}},
		{name: "etcd growing", edits: []edit{metrics(6.6*(1<<30), 100, 0, 0)}, want: want{outcome: health.Warn, message: "etcd database growing toward the 8 GiB limit (82.5%)"}},
		{name: "etcd near the limit", edits: []edit{metrics(7.7*(1<<30), 100, 0, 0)}, want: want{outcome: health.Fail, message: "near the 8 GiB read-only limit (96.2%)"}},
		{name: "server errors", edits: []edit{metrics(1<<30, 10000, 250, 0)}, want: want{outcome: health.Warn, message: "elevated API server error rate (2.50% 5xx)"}},
		{name: "throttling", edits: []edit{metrics(1<<30, 10000, 0, 150)}, want: want{outcome: health.Warn, message: "throttling requests (1.5% 429)"}},
		{name: "errors below the request floor", edits: []edit{metrics(1<<30, 100, 50, 50)}, want: want{outcome: health.Pass, message: "etcd 12% of 8 GiB"}},
		{name: "no metrics emitted", edits: []edit{func(s *fleettest.State, _ *fleettest.Cluster) {
			s.Metrics = slices.DeleteFunc(s.Metrics, func(m fleettest.Metric) bool { return m.Namespace == "AWS/EKS" })
		}}, want: want{outcome: health.Skipped, message: "not emitted"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t, tt.edits...)
			check(t, h.run(testContext(t), health.ControlPlane()), tt.want)
		})
	}
	t.Run("unreadable", func(t *testing.T) {
		h := newHarness(t)
		h.world.SetFault(fleettest.CloudWatchGetMetricData, fleettest.Fault{Deny: true})
		check(t, h.run(testContext(t), health.ControlPlane()), want{outcome: health.Unreadable, message: "could not read the control-plane metrics", failures: 1})
	})
}

func TestQuotas(t *testing.T) {
	usage := func(values ...float64) edit {
		return func(s *fleettest.State, _ *fleettest.Cluster) {
			s.Metrics = slices.DeleteFunc(s.Metrics, func(m fleettest.Metric) bool { return m.Namespace == "AWS/Usage" })
			if len(values) > 0 {
				s.Metrics = append(s.Metrics, usageMetric(values...))
			}
		}
	}
	tests := []struct {
		name  string
		edits []edit
		want  want
	}{
		{name: "headroom", want: want{outcome: health.Pass, message: "31% of the vCPU quota in use (40 of 128)"}},
		{name: "the newest minute counts", edits: []edit{usage(120, 60)}, want: want{outcome: health.Pass, message: "47% of the vCPU quota"}},
		{name: "high", edits: []edit{usage(110)}, want: want{outcome: health.Warn, message: "86% of the vCPU quota in use"}},
		{name: "nearly exhausted", edits: []edit{usage(125)}, want: want{outcome: health.Fail, message: "nearly exhausted (98% in use)"}},
		{name: "no quota", edits: []edit{func(s *fleettest.State, _ *fleettest.Cluster) { s.Quotas = nil }}, want: want{outcome: health.Skipped, message: "quota is not reported"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t, tt.edits...)
			check(t, h.run(testContext(t), health.Quotas()), tt.want)
		})
	}
	t.Run("unreadable", func(t *testing.T) {
		h := newHarness(t)
		h.world.SetFault(fleettest.ServiceQuotasGetServiceQuota, fleettest.Fault{Deny: true})
		check(t, h.run(testContext(t), health.Quotas()), want{outcome: health.Unreadable, message: "servicequotas:GetServiceQuota", failures: 1})
	})
}

// quotaWorld is the account of the roll that stalled: a quota of 8 vCPUs
// with 4 in use, and ng-a of t3.medium nodes in an Auto Scaling group in 3
// Availability Zones, with the maxUnavailable of 1 that EKS sets.
func quotaWorld(quota, usage float64) edit {
	return func(s *fleettest.State, c *fleettest.Cluster) {
		s.Quotas[0].Value = quota
		s.Metrics = slices.DeleteFunc(s.Metrics, func(m fleettest.Metric) bool { return m.Namespace == "AWS/Usage" })
		s.Metrics = append(s.Metrics, usageMetric(usage))
		for _, ng := range c.Nodegroups {
			ng.InstanceTypes = []string{"t3.medium"}
		}
		for i := range s.AutoScalingGroups {
			s.AutoScalingGroups[i].AvailabilityZones = []string{"us-east-1a", "us-east-1b", "us-east-1c"}
		}
	}
}

// ngA changes the nodegroup ng-a of the seed.
func ngA(f func(ng *fleettest.Nodegroup)) edit {
	return func(_ *fleettest.State, c *fleettest.Cluster) { f(c.Nodegroup("ng-a")) }
}

// TestQuotas_Surge counts the nodes that a roll or a scale-up launches
// against the vCPU quota.
func TestQuotas_Surge(t *testing.T) {
	const fix = ": request a higher EC2 On-Demand vCPU quota, or set the update strategy to MINIMAL"
	tests := []struct {
		name     string
		edits    []edit
		check    health.Check
		targets  []string
		together bool
		want     want
		detail   string // a part of one detail line
	}{
		{
			name: "the roll that stalled", edits: []edit{quotaWorld(8, 4)}, targets: []string{"ng-a"},
			want:   want{outcome: health.Fail, message: "rolling ng-a may launch 3 to 6 t3.medium nodes (6 to 12 vCPU) and 4 of 8 vCPU are in use" + fix},
			detail: "rolling ng-a may launch 3 to 6 t3.medium nodes, 2 vCPU each (6 to 12 vCPU)",
		},
		{
			name: "the same account with no roll", edits: []edit{quotaWorld(8, 4)},
			want: want{outcome: health.Pass, message: "50% of the vCPU quota in use (4 of 8)"},
		},
		{
			name: "room for the likely surge, not for the most", edits: []edit{quotaWorld(12, 4)}, targets: []string{"ng-a"},
			want: want{outcome: health.Warn, message: "rolling ng-a may launch 3 to 6 t3.medium nodes (6 to 12 vCPU) and 4 of 12 vCPU are in use" + fix},
		},
		{
			name: "room for the most", edits: []edit{quotaWorld(16, 4)}, targets: []string{"ng-a"},
			want: want{outcome: health.Pass, message: "25% of the vCPU quota in use (4 of 16)"},
		},
		{
			name: "maxUnavailable above twice the zones", targets: []string{"ng-a"},
			edits: []edit{quotaWorld(20, 4), ngA(func(ng *fleettest.Nodegroup) { ng.MaxUnavailable = 10 })},
			want:  want{outcome: health.Fail, message: "rolling ng-a may launch 10 t3.medium nodes (20 vCPU) and 4 of 20 vCPU are in use" + fix},
		},
		{
			name: "maxUnavailable as a percentage", targets: []string{"ng-a"},
			edits: []edit{quotaWorld(20, 4), ngA(func(ng *fleettest.Nodegroup) { ng.Desired, ng.MaxUnavailable, ng.MaxUnavailablePercentage = 20, 0, 50 })},
			want:  want{outcome: health.Fail, message: "rolling ng-a may launch 10 t3.medium nodes (20 vCPU)"},
		},
		{
			name: "the MINIMAL strategy launches no extra node", targets: []string{"ng-a"},
			edits:  []edit{quotaWorld(8, 4), ngA(func(ng *fleettest.Nodegroup) { ng.UpdateStrategy = "MINIMAL" })},
			want:   want{outcome: health.Pass, message: "50% of the vCPU quota in use (4 of 8)"},
			detail: "nodegroup ng-a uses the MINIMAL update strategy",
		},
		{
			name: "a mixed nodegroup counts its largest type", targets: []string{"ng-a"},
			edits:  []edit{quotaWorld(14, 4), ngA(func(ng *fleettest.Nodegroup) { ng.InstanceTypes = []string{"t3.medium", "m5.xlarge"} })},
			want:   want{outcome: health.Fail, message: "rolling ng-a may launch 3 to 6 nodes of up to m5.xlarge (12 to 24 vCPU) and 4 of 14 vCPU are in use" + fix},
			detail: "of its instance types (t3.medium, m5.xlarge), each node counts as the largest, m5.xlarge",
		},
		{
			name: "rolls one after another count the largest", edits: []edit{quotaWorld(14, 4)}, targets: []string{"ng-a", "ng-b"},
			want: want{outcome: health.Warn, message: "rolling ng-a may launch 3 to 6 t3.medium nodes (6 to 12 vCPU)"},
		},
		{
			name: "rolls at once add up", edits: []edit{quotaWorld(14, 4)}, targets: []string{"ng-a", "ng-b"}, together: true,
			want: want{outcome: health.Fail, message: "rolling ng-a, ng-b at once may launch 6 to 12 nodes (12 to 24 vCPU) and 4 of 14 vCPU are in use" + fix},
		},
		{
			name: "without the zones of the group, a subnet counts as a zone", targets: []string{"ng-a"},
			edits: []edit{quotaWorld(7, 4), func(s *fleettest.State, c *fleettest.Cluster) {
				s.AutoScalingGroups[0].AvailabilityZones = nil
				c.Nodegroup("ng-a").Subnets = []string{"subnet-1", "subnet-2"}
			}},
			want:   want{outcome: health.Fail, message: "rolling ng-a may launch 2 to 4 t3.medium nodes (4 to 8 vCPU)"},
			detail: "each of its subnets counts as one zone (2)",
		},
		{
			name: "Spot nodes are not counted", targets: []string{"ng-a"},
			edits:  []edit{quotaWorld(8, 4), ngA(func(ng *fleettest.Nodegroup) { ng.CapacityType = "SPOT" })},
			want:   want{outcome: health.Pass},
			detail: "nodegroup ng-a runs Spot capacity",
		},
		{
			name: "a GPU type has a quota of its own", targets: []string{"ng-a"},
			edits:  []edit{quotaWorld(8, 4), ngA(func(ng *fleettest.Nodegroup) { ng.InstanceTypes = []string{"g5.xlarge"} })},
			want:   want{outcome: health.Pass},
			detail: "count against another On-Demand quota than the Standard one",
		},
		{
			name: "the surge decides a tie", edits: []edit{quotaWorld(8, 7.8)}, targets: []string{"ng-a"},
			want: want{outcome: health.Fail, message: "rolling ng-a may launch"},
		},
		{
			name: "current use decides when it is worse", targets: []string{"ng-a"},
			edits: []edit{quotaWorld(100, 96), ngA(func(ng *fleettest.Nodegroup) { ng.UpdateStrategy = "MINIMAL" })},
			want:  want{outcome: health.Fail, message: "EC2 vCPU quota nearly exhausted (96% in use)"},
		},
		{
			name: "a scale-up launches what it adds", edits: []edit{quotaWorld(8, 4)}, check: health.ScaleUpQuotas("ng-a", 3), targets: []string{"ng-a"},
			want: want{outcome: health.Fail, message: "scaling ng-a up by 3 launches 3 t3.medium nodes (6 vCPU) and 4 of 8 vCPU are in use: " +
				"request a higher EC2 On-Demand vCPU quota, or scale up by fewer nodes"},
		},
		{
			name: "a scale-up that fits", edits: []edit{quotaWorld(8, 4)}, check: health.ScaleUpQuotas("ng-a", 2), targets: []string{"ng-a"},
			want:   want{outcome: health.Pass, message: "50% of the vCPU quota in use (4 of 8)"},
			detail: "scaling ng-a up by 2 launches 2 t3.medium nodes, 2 vCPU each (4 vCPU)",
		},
		{
			name: "a scale that adds no node", edits: []edit{quotaWorld(8, 4)}, check: health.ScaleUpQuotas("ng-a", 0), targets: []string{"ng-a"},
			want: want{outcome: health.Pass, message: "50% of the vCPU quota in use (4 of 8)"},
		},
		{
			name: "no quota", targets: []string{"ng-a"},
			edits: []edit{quotaWorld(8, 4), func(s *fleettest.State, _ *fleettest.Cluster) { s.Quotas = nil }},
			want:  want{outcome: health.Skipped, message: "quota is not reported"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t, tt.edits...)
			ctx := testContext(t)
			in := h.input(ctx, tt.targets...)
			in.RollTogether = tt.together
			c := tt.check
			if c.Run == nil {
				c = health.Quotas()
			}
			got := health.Run(ctx, []health.Check{c}, in)[0]
			check(t, got, tt.want)
			if tt.detail != "" && !slices.ContainsFunc(got.Details, func(d string) bool { return strings.Contains(d, tt.detail) }) {
				t.Errorf("details = %q, want one with %q", got.Details, tt.detail)
			}
		})
	}
	t.Run("the quota is unreadable", func(t *testing.T) {
		h := newHarness(t, quotaWorld(8, 4))
		h.world.SetFault(fleettest.ServiceQuotasGetServiceQuota, fleettest.Fault{Deny: true})
		check(t, h.run(testContext(t), health.Quotas(), "ng-a"), want{outcome: health.Unreadable, message: "servicequotas:GetServiceQuota", failures: 1})
	})
	t.Run("the instance types are unreadable", func(t *testing.T) {
		h := newHarness(t, quotaWorld(8, 4))
		h.world.SetFault(fleettest.EC2DescribeInstanceTypes, fleettest.Fault{Deny: true})
		check(t, h.run(testContext(t), health.Quotas(), "ng-a"), want{outcome: health.Unreadable, message: "could not read the vCPUs of the instance types", failures: 1})
	})
	t.Run("no instance-type read without a target", func(t *testing.T) {
		h := newHarness(t, quotaWorld(8, 4))
		h.run(testContext(t), health.Quotas())
		if n := countAWS(h.aws, fleettest.EC2DescribeInstanceTypes); n != 0 {
			t.Errorf("DescribeInstanceTypes calls = %d, want 0", n)
		}
	})
}

// instanceWorld is quotaWorld of a new account: the AWS/Usage metric has
// no data points, and the account runs 2 On-Demand t4g.small instances (4
// vCPU), a Spot t3.medium, a p3.2xlarge, and a stopped m5.2xlarge.
func instanceWorld(quota float64) edit {
	return func(s *fleettest.State, c *fleettest.Cluster) {
		quotaWorld(quota, 0)(s, c)
		s.Metrics = slices.DeleteFunc(s.Metrics, func(m fleettest.Metric) bool { return m.Namespace == "AWS/Usage" })
		for i := range s.Instances {
			s.Instances[i].Type = "t4g.small"
		}
		s.Instances[2].Type, s.Instances[2].Lifecycle = "t3.medium", "spot"
		s.Instances[3].Type = "p3.2xlarge"
		s.Instances = append(s.Instances, fleettest.Instance{ID: "i-stopped", ImageID: "ami-1", Type: "m5.2xlarge", State: "stopped"})
	}
}

// TestQuotas_InstanceList counts the vCPUs in use from the instance list
// when the AWS/Usage metric has no data points.
func TestQuotas_InstanceList(t *testing.T) {
	const source = "the vCPUs in use come from the instance list, not from CloudWatch"
	tests := []struct {
		name  string
		edits []edit
		check health.Check
		want  want
	}{
		{
			name: "a scale-up that fits", edits: []edit{instanceWorld(8)}, check: health.ScaleUpQuotas("ng-a", 2),
			want: want{outcome: health.Pass, message: "50% of the vCPU quota in use (4 of 8)"},
		},
		{
			name: "a scale-up that does not fit", edits: []edit{instanceWorld(8)}, check: health.ScaleUpQuotas("ng-a", 3),
			want: want{outcome: health.Fail, message: "scaling ng-a up by 3 launches 3 t3.medium nodes (6 vCPU) and 4 of 8 vCPU are in use"},
		},
		{
			name: "Spot, other families, and stopped instances are left out", edits: []edit{instanceWorld(100)}, check: health.Quotas(),
			want: want{outcome: health.Pass, message: "4% of the vCPU quota in use (4 of 100)"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t, tt.edits...)
			got := h.run(testContext(t), tt.check, "ng-a")
			check(t, got, tt.want)
			if !slices.ContainsFunc(got.Details, func(d string) bool { return strings.Contains(d, source) }) {
				t.Errorf("details = %q, want one with %q", got.Details, source)
			}
		})
	}
	t.Run("the list stops once the use passes the quota", func(t *testing.T) {
		h := newHarness(t, instanceWorld(8), func(s *fleettest.State, _ *fleettest.Cluster) {
			for i := range 10 {
				s.Instances = append(s.Instances, fleettest.Instance{ID: "i-big-" + strconv.Itoa(i), ImageID: "ami-1", Type: "m5.2xlarge"})
			}
		})
		ctx := testContext(t)
		in := h.input(ctx)
		h.world.SetFault(fleettest.EC2DescribeInstances, fleettest.Fault{PageSize: 1})
		before := countAWS(h.aws, fleettest.EC2DescribeInstances)
		got := health.Run(ctx, []health.Check{health.Quotas()}, in)[0]
		check(t, got, want{outcome: health.Fail, message: "nearly exhausted (150% in use)"})
		// Pages of one: 2, 2, Spot, p3, and the first m5.2xlarge passes 8.
		if n := countAWS(h.aws, fleettest.EC2DescribeInstances) - before; n != 5 {
			t.Errorf("DescribeInstances calls = %d, want 5", n)
		}
		if !slices.ContainsFunc(got.Details, func(d string) bool { return strings.Contains(d, "at least 12") }) {
			t.Errorf("details = %q, want one with %q", got.Details, "at least 12")
		}
	})
	t.Run("the instance list fails too", func(t *testing.T) {
		for _, op := range []fleettest.Operation{fleettest.EC2DescribeInstances, fleettest.EC2DescribeInstanceTypes} {
			t.Run(string(op), func(t *testing.T) {
				h := newHarness(t, instanceWorld(8))
				ctx := testContext(t)
				in := h.input(ctx)
				h.world.SetFault(op, fleettest.Fault{Deny: true})
				got := health.Run(ctx, []health.Check{health.Quotas()}, in)[0]
				check(t, got, want{outcome: health.Skipped, message: "AWS/Usage vCPU metric is not reported, and the instance list could not be read"})
			})
		}
	})
	t.Run("the metric fails", func(t *testing.T) {
		h := newHarness(t, instanceWorld(8))
		ctx := testContext(t)
		in := h.input(ctx)
		h.world.SetFault(fleettest.CloudWatchGetMetricData, fleettest.Fault{Deny: true})
		before := countAWS(h.aws, fleettest.EC2DescribeInstances)
		check(t, health.Run(ctx, []health.Check{health.Quotas()}, in)[0], want{outcome: health.Unreadable, message: "cloudwatch:GetMetricData", failures: 1})
		if n := countAWS(h.aws, fleettest.EC2DescribeInstances) - before; n != 0 {
			t.Errorf("DescribeInstances calls = %d, want 0", n)
		}
	})
	t.Run("no instance list when the metric has data", func(t *testing.T) {
		h := newHarness(t, quotaWorld(8, 4))
		ctx := testContext(t)
		in := h.input(ctx)
		before := countAWS(h.aws, fleettest.EC2DescribeInstances)
		got := health.Run(ctx, []health.Check{health.Quotas()}, in)[0]
		check(t, got, want{outcome: health.Pass, message: "50% of the vCPU quota in use (4 of 8)"})
		if n := countAWS(h.aws, fleettest.EC2DescribeInstances) - before; n != 0 {
			t.Errorf("DescribeInstances calls = %d, want 0", n)
		}
	})
}

func TestWorkloads(t *testing.T) {
	addPods := func(pods ...*fleettest.Pod) edit {
		return func(_ *fleettest.State, c *fleettest.Cluster) { c.Pods = append(c.Pods, pods...) }
	}
	manyPods := func(_ *fleettest.State, c *fleettest.Cluster) {
		for _, name := range []string{"p-1", "p-2", "p-3", "p-4", "p-5", "p-6", "p-7"} {
			c.Pods = append(c.Pods, &fleettest.Pod{Namespace: "kube-system", Name: name, Node: "a-2"})
		}
	}
	tests := []struct {
		name  string
		edits []edit
		want  want
	}{
		{name: "every pod running", want: want{outcome: health.Pass, message: "3/3 pods running"}},
		{name: "a pending pod among few", edits: []edit{addPods(&fleettest.Pod{Namespace: "kube-system", Name: "stuck", Phase: "Pending"})}, want: want{outcome: health.Fail, message: "3/4 system pods running; kube-system/stuck (Pending)", findings: []string{"kube-system/stuck"}}},
		{name: "a pending pod among many", edits: []edit{manyPods, addPods(&fleettest.Pod{Namespace: "kube-system", Name: "stuck", Phase: "Pending"})}, want: want{outcome: health.Warn, message: "10/11 system pods running", findings: []string{"kube-system/stuck"}}},
		{name: "containers not ready", edits: []edit{addPods(&fleettest.Pod{Namespace: "kube-system", Name: "slow", Node: "a-1", NotReady: true})}, want: want{outcome: health.Fail, message: "kube-system/slow (containers not ready)"}},
		{name: "a succeeded pod", edits: []edit{addPods(&fleettest.Pod{Namespace: "kube-system", Name: "job", Phase: "Succeeded"})}, want: want{outcome: health.Pass, message: "3/3 pods running"}},
		{name: "an evicted pod", edits: []edit{addPods(&fleettest.Pod{Namespace: "kube-system", Name: "old", Phase: "Failed", Reason: "Evicted"})}, want: want{outcome: health.Pass, message: "3/3"}},
		{name: "a pod ended by a node shutdown", edits: []edit{addPods(&fleettest.Pod{Namespace: "kube-system", Name: "old", Phase: "Failed", Reason: "Terminated", DisruptionTarget: true})}, want: want{outcome: health.Pass, message: "3/3"}},
		{name: "a terminated pod without shutdown evidence", edits: []edit{addPods(&fleettest.Pod{Namespace: "kube-system", Name: "old", Phase: "Failed", Reason: "Terminated"})}, want: want{outcome: health.Fail, message: "kube-system/old (Failed)"}},
		{name: "no pods", edits: []edit{func(_ *fleettest.State, c *fleettest.Cluster) { c.Pods = nil }}, want: want{outcome: health.Warn, message: "no pods in the system namespaces"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t, tt.edits...)
			check(t, h.run(testContext(t), health.Workloads()), tt.want)
		})
	}
	t.Run("unreadable", func(t *testing.T) {
		h := newHarness(t)
		h.world.SetFault(fleettest.KubeListPods, fleettest.Fault{Deny: true})
		check(t, h.run(testContext(t), health.Workloads()), want{outcome: health.Unreadable, message: "not read: kube-system, kube-public, kube-node-lease", failures: 3})
	})
	t.Run("a failed read keeps a fail", func(t *testing.T) {
		h := newHarness(t, addPods(&fleettest.Pod{Namespace: "kube-system", Name: "stuck", Phase: "Pending"}))
		// The namespaces are read in parallel, so the fault names its
		// namespace rather than following the order of the requests.
		h.world.SetScopedFault(fleettest.Scope{Operation: fleettest.KubeListPods, Namespace: "kube-public"}, fleettest.Fault{Deny: true})
		check(t, h.run(testContext(t), health.Workloads()), want{outcome: health.Fail, message: "not read: kube-public", findings: []string{"kube-system/stuck"}, failures: 1})
	})
}

func tight(name string) edit {
	return func(_ *fleettest.State, c *fleettest.Cluster) {
		for i := range c.PDBs {
			if c.PDBs[i].Name == name {
				c.PDBs[i].MinAvailable, c.PDBs[i].DisruptionsAllowed, c.PDBs[i].DesiredHealthy = 2, 0, 2
			}
		}
	}
}

func TestDrainBlockers(t *testing.T) {
	addPDB := func(p fleettest.PDB) edit {
		return func(_ *fleettest.State, c *fleettest.Cluster) { c.PDBs = append(c.PDBs, p) }
	}
	tests := []struct {
		name    string
		edits   []edit
		targets []string
		want    want
	}{
		{name: "room everywhere", targets: []string{"ng-a"}, want: want{outcome: health.Pass, message: "no PodDisruptionBudget blocks a drain"}},
		{name: "a tight budget on the target", edits: []edit{tight("web")}, targets: []string{"ng-a"}, want: want{outcome: health.Fail, message: "PDB default/web allows 0 disruptions on the target nodegroups", findings: []string{"default/web"}}},
		{name: "a tight budget elsewhere", edits: []edit{tight("api")}, targets: []string{"ng-a"}, want: want{outcome: health.Pass, message: "no PodDisruptionBudget blocks"}},
		{name: "a tight budget, unscoped", edits: []edit{tight("api")}, want: want{outcome: health.Fail, message: "PDB default/api allows 0 disruptions; a drain would stall", findings: []string{"default/api"}}},
		{name: "two targets", edits: []edit{tight("api")}, targets: []string{"ng-a", "ng-b"}, want: want{outcome: health.Fail, findings: []string{"default/api"}}},
		{name: "an unsynced budget", edits: []edit{func(_ *fleettest.State, c *fleettest.Cluster) {
			c.PDBs[0].Unsynced, c.PDBs[0].DisruptionsAllowed, c.PDBs[0].ExpectedPods = true, 0, 0
		}}, targets: []string{"ng-a"}, want: want{outcome: health.Fail, message: "PDB default/web allows 0 disruptions", findings: []string{"default/web"}}},
		{name: "a sync-failed budget", edits: []edit{func(_ *fleettest.State, c *fleettest.Cluster) {
			c.PDBs[0].SyncFailed, c.PDBs[0].DisruptionsAllowed, c.PDBs[0].ExpectedPods = true, 0, 0
		}}, targets: []string{"ng-a"}, want: want{outcome: health.Fail, findings: []string{"default/web"}}},
		{name: "a synced empty budget", edits: []edit{func(_ *fleettest.State, c *fleettest.Cluster) {
			c.PDBs[0].DisruptionsAllowed, c.PDBs[0].ExpectedPods = 0, 0
		}}, targets: []string{"ng-a"}, want: want{outcome: health.Pass}},
		{name: "a pod covered by two budgets", edits: []edit{addPDB(fleettest.PDB{Namespace: "default", Name: "web-too", Selector: map[string]string{"app": "web"}, MinAvailable: 1, DisruptionsAllowed: 1, CurrentHealthy: 2, DesiredHealthy: 1, ExpectedPods: 2})},
			targets: []string{"ng-a"}, want: want{outcome: health.Fail, message: "2 pods covered by more than one PDB", findings: []string{"default/web-1", "default/web-2"}}},
		{name: "a multi-budget pod elsewhere", edits: []edit{addPDB(fleettest.PDB{Namespace: "default", Name: "api-too", Selector: map[string]string{"app": "api"}, MinAvailable: 1, DisruptionsAllowed: 1, CurrentHealthy: 2, DesiredHealthy: 1, ExpectedPods: 2})},
			targets: []string{"ng-a"}, want: want{outcome: health.Pass}},
		{name: "a not-ready pod under AlwaysAllow", edits: []edit{tight("web"), func(_ *fleettest.State, c *fleettest.Cluster) {
			c.PDBs[0].AlwaysAllow = true
			c.Pods[3].NotReady, c.Pods[4].NotReady = true, true
		}}, targets: []string{"ng-a"}, want: want{outcome: health.Pass}},
		{name: "a not-ready pod under IfHealthyBudget", edits: []edit{tight("web"), func(_ *fleettest.State, c *fleettest.Cluster) {
			c.Pods[3].NotReady, c.Pods[4].NotReady = true, true
			c.PDBs[0].CurrentHealthy = 0
		}}, targets: []string{"ng-a"},
			want: want{outcome: health.Fail, findings: []string{"default/web"}}},
		{name: "pods that eviction ignores", edits: []edit{tight("web"), func(_ *fleettest.State, c *fleettest.Cluster) { c.Pods[3].Phase, c.Pods[4].Deleting = "Pending", true }}, targets: []string{"ng-a"}, want: want{outcome: health.Pass}},
		{name: "a budget with no selector", edits: []edit{tight("web"), func(_ *fleettest.State, c *fleettest.Cluster) { c.PDBs[0].NoSelector = true }}, targets: []string{"ng-a"}, want: want{outcome: health.Pass}},
		{name: "a budget with no selector, unscoped", edits: []edit{tight("web"), func(_ *fleettest.State, c *fleettest.Cluster) { c.PDBs[0].NoSelector = true }}, want: want{outcome: health.Fail, findings: []string{"default/web"}}},
		{name: "a target scaled to zero", edits: []edit{tight("web"), func(_ *fleettest.State, c *fleettest.Cluster) {
			c.Nodegroup("ng-a").Desired, c.Nodegroup("ng-a").Min, c.Nodegroup("ng-a").Max = 0, 0, 3
			c.Nodes = c.Nodes[2:]
		}}, targets: []string{"ng-a"}, want: want{outcome: health.Pass, message: "nothing to drain"}},
		{name: "a target with no labelled node falls back", edits: []edit{tight("api"), func(_ *fleettest.State, c *fleettest.Cluster) { c.Nodes = c.Nodes[2:] }}, targets: []string{"ng-a"}, want: want{outcome: health.Fail, findings: []string{"default/api"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t, tt.edits...)
			check(t, h.run(testContext(t), health.DrainBlockers(), tt.targets...), tt.want)
		})
	}
	t.Run("unscoped, an at-risk budget needs no pod read", func(t *testing.T) {
		// One budget in the namespace: no pod list at all, and a denied
		// pod list cannot hide the blocker.
		h := newHarness(t, tight("api"), func(_ *fleettest.State, c *fleettest.Cluster) { c.PDBs = c.PDBs[1:] })
		h.world.SetFault(fleettest.KubeListPods, fleettest.Fault{Deny: true})
		check(t, h.run(testContext(t), health.DrainBlockers()), want{outcome: health.Fail, findings: []string{"default/api"}})
		if n := count(h.kube.Requests(), fleettest.KubeListPods); n != 0 {
			t.Errorf("pod lists = %d, want none", n)
		}
		// Two budgets: the pods are read for the multi-budget rule, and
		// the blocker is reported with the failed read beside it.
		h = newHarness(t, tight("api"))
		h.world.SetFault(fleettest.KubeListPods, fleettest.Fault{Deny: true})
		check(t, h.run(testContext(t), health.DrainBlockers()), want{outcome: health.Fail, message: "not read: default", findings: []string{"default/api"}, failures: 1})
	})
	t.Run("unreadable", func(t *testing.T) {
		h := newHarness(t)
		h.world.SetFault(fleettest.KubeListPDBs, fleettest.Fault{Deny: true})
		check(t, h.run(testContext(t), health.DrainBlockers(), "ng-a"), want{outcome: health.Unreadable, message: "could not list the PodDisruptionBudgets", failures: 1})
		h = newHarness(t)
		h.world.SetFault(fleettest.KubeListNodes, fleettest.Fault{Deny: true})
		check(t, h.run(testContext(t), health.DrainBlockers(), "ng-a"), want{outcome: health.Unreadable, message: "could not list the nodes", failures: 1})
		h = newHarness(t, tight("web"))
		h.world.SetFault(fleettest.KubeListPods, fleettest.Fault{Deny: true})
		check(t, h.run(testContext(t), health.DrainBlockers(), "ng-a"), want{outcome: health.Unreadable, message: "could not list the pods of default", failures: 1})
	})
}

func TestScaleDownBlockers(t *testing.T) {
	tests := []struct {
		name   string
		edits  []edit
		remove int32
		want   want
	}{
		{name: "a scale-up", remove: 0, want: want{outcome: health.Pass, message: "a scale-up removes no node"}},
		{name: "one node with room", remove: 1, want: want{outcome: health.Pass, message: "no PodDisruptionBudget stops removing 1 node from ng-b"}},
		{name: "two nodes lose more than allowed", remove: 2, want: want{outcome: health.Fail, message: "removing 2 nodes from ng-b could evict more pods than PDB default/api allows", findings: []string{"default/api"}}},
		{name: "a tight budget", edits: []edit{tight("api")}, remove: 1, want: want{outcome: health.Fail, findings: []string{"default/api"}}},
		{name: "a tight budget elsewhere", edits: []edit{tight("web")}, remove: 1, want: want{outcome: health.Pass}},
		{name: "a pod with two budgets is not a scale-down blocker", remove: 1, edits: []edit{func(_ *fleettest.State, c *fleettest.Cluster) {
			c.PDBs = append(c.PDBs, fleettest.PDB{Namespace: "default", Name: "api-too", Selector: map[string]string{"app": "api"}, MinAvailable: 1, DisruptionsAllowed: 1, CurrentHealthy: 2, DesiredHealthy: 1, ExpectedPods: 2})
		}}, want: want{outcome: health.Pass}},
		{name: "the fallback without nodes drops multi-budget pods too", remove: 1, edits: []edit{tight("web"), func(_ *fleettest.State, c *fleettest.Cluster) {
			c.Nodes = c.Nodes[:2]
			c.PDBs = append(c.PDBs, fleettest.PDB{Namespace: "default", Name: "api-too", Selector: map[string]string{"app": "api"}, MinAvailable: 1, DisruptionsAllowed: 1, CurrentHealthy: 2, DesiredHealthy: 1, ExpectedPods: 2})
		}}, want: want{outcome: health.Fail, findings: []string{"default/web"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t, tt.edits...)
			check(t, h.run(testContext(t), health.ScaleDownBlockers("ng-b", tt.remove)), tt.want)
			// The pods of the budgets' namespaces are read in one wave: one
			// list for the one namespace, whatever the number of budgets.
			if n := count(h.kube.Requests(), fleettest.KubeListPods); n > 1 {
				t.Errorf("pod lists = %d, want at most 1", n)
			}
		})
	}
}

func TestPDBCoverage(t *testing.T) {
	tests := []struct {
		name  string
		edits []edit
		want  want
	}{
		{name: "every deployment covered", want: want{outcome: health.Pass, message: "every deployment has a PodDisruptionBudget (2)"}},
		{name: "an uncovered deployment", edits: []edit{func(_ *fleettest.State, c *fleettest.Cluster) {
			c.Deployments = append(c.Deployments, fleettest.Deployment{Namespace: "default", Name: "batch", Replicas: 1, Selector: map[string]string{"app": "batch"}})
		}}, want: want{outcome: health.Warn, message: "1 of 3 deployments without a PodDisruptionBudget: default/batch", findings: []string{"default/batch"}}},
		{name: "a system deployment does not count", edits: []edit{func(_ *fleettest.State, c *fleettest.Cluster) { c.Deployments = c.Deployments[2:] }}, want: want{outcome: health.Pass, message: "no user deployments"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t, tt.edits...)
			check(t, h.run(testContext(t), health.PDBCoverage()), tt.want)
		})
	}
	t.Run("unreadable", func(t *testing.T) {
		h := newHarness(t)
		h.world.SetFault(fleettest.KubeListDeployments, fleettest.Fault{Deny: true})
		check(t, h.run(testContext(t), health.PDBCoverage()), want{outcome: health.Unreadable, message: "could not list the deployments", failures: 1})
	})
}

func TestUtilization(t *testing.T) {
	use := func(milliCPU, memoryMi int64) edit {
		return func(_ *fleettest.State, c *fleettest.Cluster) {
			for _, n := range c.Nodes {
				n.UsedMilliCPU, n.UsedMemoryMi = milliCPU, memoryMi
			}
		}
	}
	tests := []struct {
		name  string
		edits []edit
		want  want
	}{
		{name: "headroom", want: want{outcome: health.Pass, message: "CPU 20%, memory 25% in use"}},
		{name: "limited memory", edits: []edit{use(400, 7000)}, want: want{outcome: health.Warn, message: "limited headroom for a drain (CPU 20%, memory 85% in use)"}},
		{name: "limited cpu", edits: []edit{use(1500, 2048)}, want: want{outcome: health.Warn, message: "CPU 75%"}},
		{name: "low memory", edits: []edit{use(400, 7500)}, want: want{outcome: health.Fail, message: "low headroom for a drain"}},
		{name: "no metrics-server", edits: []edit{func(_ *fleettest.State, c *fleettest.Cluster) { c.NoMetricsServer = true }}, want: want{outcome: health.Skipped, message: "metrics-server is not installed"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t, tt.edits...)
			check(t, h.run(testContext(t), health.Utilization()), tt.want)
		})
	}
	t.Run("unreadable", func(t *testing.T) {
		h := newHarness(t)
		h.world.SetFault(fleettest.KubeListNodeMetrics, fleettest.Fault{Deny: true})
		check(t, h.run(testContext(t), health.Utilization()), want{outcome: health.Unreadable, message: "could not read the node metrics", failures: 1})
	})
	t.Run("no metrics client", func(t *testing.T) {
		h := newHarness(t)
		ctx := testContext(t)
		in := h.input(ctx)
		in.Metrics = nil
		check(t, health.Run(ctx, []health.Check{health.Utilization()}, in)[0], want{outcome: health.Skipped, message: "no metrics client"})
	})
}

// TestRun covers the runner: the needs, the access result, the shared
// reads, and a cancelled context.
func TestRun(t *testing.T) {
	t.Run("the whole set passes", testRunWholeSet)
	t.Run("no Kubernetes", testRunNoKube)
	t.Run("an unreachable API", testRunUnreachable)
	t.Run("the run's deadline", testRunDeadline)
	t.Run("the client's timeout on one list", testRunClientTimeout)
	t.Run("a server error", testRunServerError)
	t.Run("a forbidden API", testRunForbidden)
	t.Run("a cancelled context", testRunCancelled)
}

func testRunWholeSet(t *testing.T) {
	h := newHarness(t)
	ctx := testContext(t)
	results := health.Run(ctx, health.Preflight(), h.input(ctx, "ng-a"))
	var names []string
	for _, r := range results {
		names = append(names, r.Name)
		if r.Outcome != health.Pass {
			t.Errorf("%s: %s: %s", r.Name, r.Outcome, r.Message)
		}
	}
	want := []string{"Nodes", "Capacity", "ControlPlane", "Workloads", "DrainBlockers", "PDBCoverage", "Utilization", "Quotas", health.AccessCheck}
	if diff := cmp.Diff(want, names); diff != "" {
		t.Errorf("names (-want +got):\n%s", diff)
	}
	// One node list for the three checks that read it, and one metric
	// call for each of the three metric checks.
	if n := count(h.kube.Requests(), fleettest.KubeListNodes); n != 1 {
		t.Errorf("node lists = %d, want 1", n)
	}
	if n := countAWS(h.aws, fleettest.CloudWatchGetMetricData); n != 3 {
		t.Errorf("GetMetricData calls = %d, want 3", n)
	}
	// The three system namespaces, and default, which has two budgets.
	if n := count(h.kube.Requests(), fleettest.KubeListPods); n != 4 {
		t.Errorf("pod lists = %d, want 4", n)
	}
	if v := health.Gate(results, health.Policy{}); v.Decision != health.Go || v.Exit != 0 {
		t.Errorf("verdict = %+v, want Go", v)
	}
}

func testRunNoKube(t *testing.T) {
	h := newHarness(t)
	ctx := testContext(t)
	in := h.input(ctx)
	in.Kube, in.Metrics = nil, nil
	results := health.Run(ctx, health.Preflight(), in)
	for _, r := range results {
		switch r.Name {
		case "Workloads", "DrainBlockers", "PDBCoverage", "Utilization":
			check(t, r, want{outcome: health.Skipped, message: "no kubeconfig context points at prod"})
		case health.AccessCheck:
			check(t, r, want{outcome: health.Warn, message: "no Kubernetes access: no kubeconfig context points at prod; the Kubernetes checks did not run"})
		}
	}
	if v := health.Gate(results, health.Policy{}); v.Decision != health.Ask || v.Exit != 2 {
		t.Errorf("verdict = %+v, want Ask with exit 2", v)
	}
}

func testRunUnreachable(t *testing.T) {
	// The API server is gone: every read is refused at the connection, and
	// nothing waits on a clock.
	h := newHarness(t)
	ctx := testContext(t)
	in := h.input(ctx, "ng-a")
	h.kube.Close()
	results := health.Run(ctx, health.Preflight(), in)
	for _, r := range results {
		switch r.Name {
		case "Nodes", "Workloads", "DrainBlockers", "PDBCoverage", "Utilization":
			// The node check too: its read did not connect, and there is
			// no estimate for a client that is there and does not answer.
			check(t, r, want{outcome: health.Skipped, message: "Kubernetes API unreachable"})
		case health.AccessCheck:
			check(t, r, want{outcome: health.Warn, message: "Kubernetes API unreachable"})
		}
	}
	if v := health.Gate(results, health.Policy{}); v.Decision != health.Ask {
		t.Errorf("verdict = %+v, want Ask", v)
	}
}

// The run's deadline passes while the reads are in flight: that is not an
// unreachable API. The checks stay Unreadable with their failures, and a
// fail-closed check blocks.
func testRunDeadline(t *testing.T) {
	h := newHarness(t)
	ctx, expire := awsxtest.Deadline(testContext(t))
	in := h.input(ctx, "ng-a")
	h.kube.OnRequest = func(hctx context.Context, _ kubetest.Request) {
		expire()
		<-hctx.Done()
	}
	results := health.Run(ctx, health.Preflight(), in)
	for _, r := range results {
		switch r.Name {
		case "Nodes", "Workloads", "DrainBlockers", "PDBCoverage", "Utilization":
			if r.Outcome != health.Unreadable || len(r.Failures) == 0 || r.Failures[0].Reason != apidoc.ReasonTimeout {
				t.Errorf("%s: %s %+v, want Unreadable with a Timeout failure", r.Name, r.Outcome, r.Failures)
			}
		case health.AccessCheck:
			check(t, r, want{outcome: health.Pass})
		}
	}
	if v := health.Gate(results, health.Policy{Yes: true}); v.Decision != health.Block || v.Exit != 4 {
		t.Errorf("verdict = %+v, want Block with exit 4", v)
	}
}

// The client's own timeout on one list, after another list answered: the
// check that needed it is Unreadable with a Timeout failure, and the API
// is not unreachable.
func testRunClientTimeout(t *testing.T) {
	h := newHarness(t)
	ctx := testContext(t)
	in := h.input(ctx, "ng-a")
	cfg := h.kube.Config()
	cfg.Timeout = 100 * time.Millisecond
	client, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	in.Kube = client
	h.world.SetFault(fleettest.KubeListPDBs, fleettest.Fault{Hang: true})
	results := health.Run(ctx, []health.Check{health.Nodes(), health.DrainBlockers()}, in)
	check(t, results[0], want{outcome: health.Pass})
	if r := results[1]; r.Outcome != health.Unreadable || len(r.Failures) != 1 || r.Failures[0].Reason != apidoc.ReasonTimeout {
		t.Errorf("DrainBlockers = %s %+v, want Unreadable with a Timeout failure", r.Outcome, r.Failures)
	}
	check(t, results[2], want{outcome: health.Pass, message: "reachable"})
	if v := health.Gate(results, health.Policy{Yes: true}); v.Decision != health.Block || v.Exit != 4 {
		t.Errorf("verdict = %+v, want Block with exit 4", v)
	}
}

// A server error on every read is an answer: Unreadable, not unreachable.
func testRunServerError(t *testing.T) {
	h := newHarness(t)
	h.world.SetScopedFault(fleettest.Scope{Service: "kube"}, fleettest.Fault{Code: "InternalError", Status: 500})
	ctx := testContext(t)
	results := health.Run(ctx, health.Preflight(), h.input(ctx, "ng-a"))
	for _, r := range results {
		switch r.Name {
		case "Nodes", "Workloads", "DrainBlockers", "PDBCoverage", "Utilization":
			if r.Outcome != health.Unreadable || len(r.Failures) == 0 || r.Failures[0].Reason != apidoc.ReasonServiceError {
				t.Errorf("%s: %s %+v, want Unreadable with a ServiceError failure", r.Name, r.Outcome, r.Failures)
			}
		case health.AccessCheck:
			check(t, r, want{outcome: health.Pass, message: "reachable"})
		}
	}
	if v := health.Gate(results, health.Policy{Yes: true}); v.Decision != health.Block {
		t.Errorf("verdict = %+v, want Block", v)
	}
}

func testRunForbidden(t *testing.T) {
	h := newHarness(t)
	h.world.SetScopedFault(fleettest.Scope{Service: "kube"}, fleettest.Fault{Deny: true})
	ctx := testContext(t)
	results := health.Run(ctx, health.Preflight(), h.input(ctx, "ng-a"))
	for _, r := range results {
		switch r.Name {
		case "Nodes", "Workloads", "DrainBlockers", "PDBCoverage", "Utilization":
			if r.Outcome != health.Unreadable {
				t.Errorf("%s: %s, want Unreadable", r.Name, r.Outcome)
			}
		case health.AccessCheck:
			check(t, r, want{outcome: health.Pass, message: "reachable"})
		}
	}
	if v := health.Gate(results, health.Policy{Yes: true}); v.Decision != health.Block || v.Exit != 4 {
		t.Errorf("verdict = %+v, want Block with exit 4", v)
	}
}

func testRunCancelled(t *testing.T) {
	h := newHarness(t)
	ctx, cancel := context.WithCancel(testContext(t))
	in := h.input(ctx)
	cancel()
	results := health.Run(ctx, health.Preflight(), in)
	for _, r := range results[:len(results)-1] {
		if r.Outcome != health.Unreadable && r.Outcome != health.Skipped {
			t.Errorf("%s: %s (%s), want Unreadable", r.Name, r.Outcome, r.Message)
		}
	}
}

func count(requests []kubetest.Request, op fleettest.Operation) int {
	n := 0
	for _, r := range requests {
		if r.Operation == op {
			n++
		}
	}
	return n
}

func countAWS(srv *awsxtest.Server, op fleettest.Operation) int {
	n := 0
	for _, r := range srv.Requests() {
		if r.Operation == op {
			n++
		}
	}
	return n
}
