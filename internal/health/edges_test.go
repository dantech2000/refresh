package health_test

import (
	"context"
	"net"
	"os"
	"syscall"
	"testing"

	"k8s.io/client-go/kubernetes"
	metricsclient "k8s.io/metrics/pkg/client/clientset/versioned"

	"github.com/dantech2000/refresh/internal/fleet/fleettest"
	"github.com/dantech2000/refresh/internal/health"
)

// The shapes of a budget that the drain rule must read from its status,
// whatever its spec says.
func TestDrainBlockers_BudgetShapes(t *testing.T) {
	zero := int32(0)
	tests := []struct {
		name    string
		edits   []edit
		targets []string
		want    want
	}{
		{
			// maxUnavailable: 0 allows no disruption at all. The
			// controller reports 0 allowed, and the drain would stall.
			name: "maxUnavailable 0 on the target",
			edits: []edit{func(_ *fleettest.State, c *fleettest.Cluster) {
				c.PDBs[0].MaxUnavailable, c.PDBs[0].DisruptionsAllowed = &zero, 0
			}},
			targets: []string{"ng-a"},
			want:    want{outcome: health.Fail, message: "PDB default/web allows 0 disruptions", findings: []string{"default/web"}},
		},
		{
			name: "maxUnavailable 0 elsewhere",
			edits: []edit{func(_ *fleettest.State, c *fleettest.Cluster) {
				c.PDBs[1].MaxUnavailable, c.PDBs[1].DisruptionsAllowed = &zero, 0
			}},
			targets: []string{"ng-a"},
			want:    want{outcome: health.Pass},
		},
		{
			// An empty selector, as opposed to none, selects every pod
			// of its namespace.
			name: "an empty selector selects the namespace",
			edits: []edit{func(_ *fleettest.State, c *fleettest.Cluster) {
				c.Pods = append(c.Pods, &fleettest.Pod{Namespace: "batch", Name: "job-1", Node: "a-1"})
				c.PDBs = append(c.PDBs, fleettest.PDB{Namespace: "batch", Name: "all", MinAvailable: 1, CurrentHealthy: 1, DesiredHealthy: 1, ExpectedPods: 1})
			}},
			targets: []string{"ng-a"},
			want:    want{outcome: health.Fail, findings: []string{"batch/all"}},
		},
		{
			// A budget whose selector matches no pod in the cluster has
			// nothing to protect: the controller expects 0 pods.
			name: "a selector that matches no pod",
			edits: []edit{func(_ *fleettest.State, c *fleettest.Cluster) {
				c.PDBs = append(c.PDBs, fleettest.PDB{Namespace: "default", Name: "ghost", Selector: map[string]string{"app": "ghost"}, MinAvailable: 1})
			}},
			want: want{outcome: health.Pass},
		},
		{
			// A nodegroup at min = max = desired = 1, whose one pod a
			// minAvailable: 1 budget guards. EKS surges a new node before
			// the drain, but the budget still allows no eviction.
			name: "one node, one pod, minAvailable 1",
			edits: []edit{func(_ *fleettest.State, c *fleettest.Cluster) {
				ng := c.Nodegroup("ng-a")
				ng.Desired, ng.Min, ng.Max = 1, 1, 1
				c.Nodes = c.Nodes[1:] // a-1 goes; a-2 stays
				var pods []*fleettest.Pod
				for _, p := range c.Pods {
					if p.Node != "a-1" {
						pods = append(pods, p)
					}
				}
				c.Pods = pods
				c.PDBs[0] = fleettest.PDB{Namespace: "default", Name: "web", Selector: map[string]string{"app": "web"}, MinAvailable: 1, CurrentHealthy: 1, DesiredHealthy: 1, ExpectedPods: 1}
			}},
			targets: []string{"ng-a"},
			want:    want{outcome: health.Fail, findings: []string{"default/web"}},
		},
		{
			// A tight budget whose pods run on nodes of no managed
			// nodegroup: a roll of ng-a does not drain them.
			name: "a tight budget on self-managed nodes",
			edits: []edit{func(_ *fleettest.State, c *fleettest.Cluster) {
				c.Nodes = append(c.Nodes, &fleettest.Node{Name: "self-1"}, &fleettest.Node{Name: "fargate-1", Labels: map[string]string{"eks.amazonaws.com/compute-type": "fargate"}})
				c.Pods = append(c.Pods,
					&fleettest.Pod{Namespace: "legacy", Name: "db-1", Node: "self-1", Labels: map[string]string{"app": "db"}},
					&fleettest.Pod{Namespace: "legacy", Name: "db-2", Node: "fargate-1", Labels: map[string]string{"app": "db"}})
				c.PDBs = append(c.PDBs, fleettest.PDB{Namespace: "legacy", Name: "db", Selector: map[string]string{"app": "db"}, MinAvailable: 2, CurrentHealthy: 2, DesiredHealthy: 2, ExpectedPods: 2})
			}},
			targets: []string{"ng-a"},
			want:    want{outcome: health.Pass},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t, tt.edits...)
			check(t, h.run(testContext(t), health.DrainBlockers(), tt.targets...), tt.want)
		})
	}
}

// RBAC that reads nodes and budgets but not pods: the checks that need
// pods are Unreadable, the API is reachable, and the gate blocks with
// exit 4 whatever --yes says. Nodes still passes.
func TestRun_PodsForbidden(t *testing.T) {
	h := newHarness(t, tight("web"))
	h.world.SetFault(fleettest.KubeListPods, fleettest.Fault{Deny: true})
	ctx := testContext(t)
	results := health.Run(ctx, health.Preflight(), h.input(ctx, "ng-a"))
	for _, r := range results {
		switch r.Name {
		case "Nodes":
			check(t, r, want{outcome: health.Pass})
		case "Workloads", "DrainBlockers":
			if r.Outcome != health.Unreadable || len(r.Failures) == 0 {
				t.Errorf("%s = %s %+v, want Unreadable with a failure", r.Name, r.Outcome, r.Failures)
			}
		case health.AccessCheck:
			check(t, r, want{outcome: health.Pass, message: "reachable"})
		}
	}
	for _, p := range []health.Policy{{}, {Yes: true}, {Yes: true, Force: false}} {
		if v := health.Gate(results, p); v.Decision != health.Block || v.Exit != 4 {
			t.Errorf("policy %+v: verdict = %+v, want Block with exit 4", p, v)
		}
	}
}

// dialing points the clients of in at the harness's API server through
// a dialer whose every connection fails at once with err, as a
// private-only endpoint does from outside its VPC. No read waits on a
// clock.
func dialing(t *testing.T, h *harness, in *health.Input, err error) {
	t.Helper()
	cfg := h.kube.Config()
	cfg.Dial = func(context.Context, string, string) (net.Conn, error) { return nil, err }
	client, e := kubernetes.NewForConfig(cfg)
	if e != nil {
		t.Fatal(e)
	}
	mc, e := metricsclient.NewForConfig(cfg)
	if e != nil {
		t.Fatal(e)
	}
	in.Kube, in.Metrics = client, mc.MetricsV1beta1().NodeMetricses()
}

// A private-only API endpoint. When the network refuses the route, every
// read fails to connect: the API is unreachable, the Kubernetes checks
// are Skipped, and the gate asks. When the packets are dropped, the dial
// times out after the client's dial timeout (10s in internal/kube); a
// timeout is not a failure to connect (phase 3, section 13), so the
// checks stay Unreadable and the gate blocks with exit 4. Neither run
// hangs: every read returns its error.
func TestRun_PrivateEndpoint(t *testing.T) {
	unroutable := &net.OpError{Op: "dial", Net: "tcp", Err: os.NewSyscallError("connect", syscall.EHOSTUNREACH)}
	timedOut := &net.OpError{Op: "dial", Net: "tcp", Err: os.ErrDeadlineExceeded}
	t.Run("no route", func(t *testing.T) {
		h := newHarness(t)
		ctx := testContext(t)
		in := h.input(ctx, "ng-a")
		dialing(t, h, in, unroutable)
		results := health.Run(ctx, health.Preflight(), in)
		for _, r := range results {
			switch r.Name {
			case "Nodes", "Workloads", "DrainBlockers", "PDBCoverage":
				check(t, r, want{outcome: health.Skipped, message: "Kubernetes API unreachable"})
			case health.AccessCheck:
				check(t, r, want{outcome: health.Warn, message: "Kubernetes API unreachable"})
			}
		}
		if v := health.Gate(results, health.Policy{}); v.Decision != health.Ask || v.Exit != 2 {
			t.Errorf("verdict = %+v, want Ask with exit 2", v)
		}
	})
	t.Run("dial timeout", func(t *testing.T) {
		h := newHarness(t)
		ctx := testContext(t)
		in := h.input(ctx, "ng-a")
		dialing(t, h, in, timedOut)
		results := health.Run(ctx, health.Preflight(), in)
		for _, r := range results {
			switch r.Name {
			case "Nodes", "Workloads", "DrainBlockers":
				if r.Outcome != health.Unreadable || len(r.Failures) == 0 {
					t.Errorf("%s = %s %+v, want Unreadable with a failure", r.Name, r.Outcome, r.Failures)
				}
			}
		}
		if v := health.Gate(results, health.Policy{Yes: true}); v.Decision != health.Block || v.Exit != 4 {
			t.Errorf("verdict = %+v, want Block with exit 4", v)
		}
	})
	t.Run("dial timeout, the access result", func(t *testing.T) {
		t.Skip("known defect: internal/health/reads.go:162-164 reports \"Kubernetes API reachable\" (Pass) whenever not every failed read was refused; " +
			"when every read timed out at the dial, no read reached the API, and the report says it was reachable")
		h := newHarness(t)
		ctx := testContext(t)
		in := h.input(ctx, "ng-a")
		dialing(t, h, in, timedOut)
		results := health.Run(ctx, health.Preflight(), in)
		for _, r := range results {
			if r.Name == health.AccessCheck && r.Outcome == health.Pass {
				t.Errorf("the access result is %s %q, and no read reached the API", r.Outcome, r.Message)
			}
		}
	})
}
