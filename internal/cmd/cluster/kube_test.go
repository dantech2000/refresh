package cluster_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/dantech2000/refresh/internal/apidoc"
	"github.com/dantech2000/refresh/internal/cmd/cmdtest"
	"github.com/dantech2000/refresh/internal/cmdutil"
	"github.com/dantech2000/refresh/internal/cmdutil/cmdutiltest"
	"github.com/dantech2000/refresh/internal/fleet/fleettest"
	"github.com/dantech2000/refresh/internal/kube/kubetest"
)

// withKube gives the cluster prod of a fake a Kubernetes API, and points
// $KUBECONFIG at a file whose one context names it. The nodes and pods
// come from the world.
func withKube(t *testing.T, e *cmdtest.Fake) *kubetest.Server {
	t.Helper()
	srv := kubetest.NewServer(e.World, east, "prod")
	t.Cleanup(srv.Close)
	path := filepath.Join(t.TempDir(), "kubeconfig")
	if err := srv.WriteKubeconfig(path); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KUBECONFIG", path)
	return srv
}

// newReadinessFake is the describe fake with nodes, pods, and budgets.
func newReadinessFake(t *testing.T) *cmdtest.Fake {
	t.Helper()
	e := newDescribeFake(t)
	e.World.Edit(func(s *fleettest.State) {
		c := s.Cluster(east, "prod")
		c.Nodes = []*fleettest.Node{
			{Name: "web-1", Nodegroup: "web"}, {Name: "web-2", Nodegroup: "web"}, {Name: "web-3", Nodegroup: "web", NotReady: true},
			{Name: "old-1", Nodegroup: "old"},
		}
		c.Pods = []*fleettest.Pod{
			{Namespace: "kube-system", Name: "coredns-1", Node: "web-1"},
			{Namespace: "default", Name: "app-1", Node: "web-1", Labels: map[string]string{"app": "app"}},
		}
		c.PDBs = []fleettest.PDB{{Namespace: "default", Name: "app", Selector: map[string]string{"app": "app"}, MinAvailable: 1, DisruptionsAllowed: 0, CurrentHealthy: 1, DesiredHealthy: 1, ExpectedPods: 1}}
		c.Deployments = []fleettest.Deployment{{Namespace: "default", Name: "app", Replicas: 1, Selector: map[string]string{"app": "app"}}}
	})
	return e
}

func TestDescribe_CheckReadiness(t *testing.T) {
	for _, tt := range []struct{ format, file string }{{"table", "describe-readiness.txt"}, {"json", "describe-readiness.json"}} {
		t.Run(tt.format, func(t *testing.T) {
			e := newReadinessFake(t)
			srv := withKube(t, e)
			if err := e.Run(cmdutiltest.Context(t), "cluster", "describe", "prod", "--check-readiness", "-o", tt.format); err != nil {
				t.Fatalf("run: %v", err)
			}
			if e.Err.Len() != 0 {
				t.Errorf("stderr = %q, want nothing", e.Err.String())
			}
			// The endpoint is the fake API server, on a port of its own.
			out := strings.ReplaceAll(e.Out.String(), srv.URL, "http://kube.fake.invalid")
			cmdutiltest.Golden(t, tt.file, out, *update)
			if tt.format == "json" {
				cmdutiltest.ValidateJSON(t, apidoc.KindClusterDescription, e.Out.Bytes())
			}
		})
	}
}

// Without a kubeconfig context for the cluster, --check-readiness warns
// once on stderr and the Kubernetes checks are skipped.
func TestDescribe_CheckReadinessWithoutAccess(t *testing.T) {
	e := newReadinessFake(t)
	if err := e.Run(cmdutiltest.Context(t), "cluster", "describe", "prod", "--check-readiness", "-o", "plain"); err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := e.Err.String(); !strings.HasPrefix(got, "warning: no Kubernetes access to prod: ") || strings.Count(got, "warning:") != 1 {
		t.Errorf("stderr = %q, want one warning", got)
	}
	if !strings.Contains(e.Out.String(), "health.Workloads.outcome\tSkipped\n") {
		t.Errorf("stdout:\n%s", e.Out.String())
	}
}

// A Kubernetes API that answers 403 makes the checks that need it
// unreadable: a failure in the document, exit 4.
func TestDescribe_CheckReadinessForbidden(t *testing.T) {
	e := newReadinessFake(t)
	withKube(t, e)
	e.World.SetScopedFault(fleettest.Scope{Service: "kube"}, fleettest.Fault{Deny: true})
	err := e.Run(cmdutiltest.Context(t), "cluster", "describe", "prod", "--check-readiness", "-o", "json")
	if got := cmdutil.ExitCode(err); got != cmdutil.ExitIncomplete {
		t.Errorf("exit code = %d (%v), want 4", got, err)
	}
	if !strings.Contains(e.Out.String(), `"outcome": "Unreadable"`) || !strings.Contains(e.Err.String(), "listing nodes") {
		t.Errorf("stdout:\n%s\nstderr:\n%s", e.Out.String(), e.Err.String())
	}
}

func TestList_ShowHealth(t *testing.T) {
	world := newWorld()
	world.Edit(func(s *fleettest.State) {
		s.Cluster(east, "prod-payments").Nodegroups = []*fleettest.Nodegroup{{Name: "web", Version: "1.33", Desired: 2}}
		s.Metrics = controlPlaneMetrics("prod-payments", 7.7*(1<<30), 100, 0, 0)
	})
	e := newEnv(t, world)
	err := e.run(testContext(t), "--show-health", "-o", "table")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	golden(t, "list-health.txt", e.out.String())
	// Every cluster that was read gets the AWS-only checks: its nodegroups
	// and groups in the sweep, then the metrics and the quota.
	for op, want := range map[fleettest.Operation]int{
		fleettest.EKSListNodegroups: 4, fleettest.EKSDescribeNodegroup: 1, fleettest.CloudWatchGetMetricData: 8, fleettest.ServiceQuotasGetServiceQuota: 4,
	} {
		if got := e.Count(op); got != want {
			t.Errorf("%s calls = %d, want %d", op, got, want)
		}
	}
	e = newEnv(t, world)
	if err := e.run(testContext(t), "--show-health", "-o", "json"); err != nil {
		t.Fatalf("run: %v", err)
	}
	validate(t, e.out.Bytes())
	if !strings.Contains(e.out.String(), `"summary": "Fail"`) {
		t.Errorf("stdout has no failed health:\n%s", e.out.String())
	}
}

// With --check-readiness the Kubernetes reads are two waves, next to the
// AWS waves: the lists that depend on nothing, then the pods of the
// namespaces that a budget or a second budget sends the check to.
func TestDescribe_CheckReadinessWaves(t *testing.T) {
	e := newReadinessFake(t)
	srv := withKube(t, e)
	k8s := kubetest.NewGate(t, srv.Handler)
	done := make(chan error, 1)
	go func() {
		done <- e.Run(cmdutiltest.Context(t), "cluster", "describe", "prod", "--check-readiness", "-o", "json")
	}()
	k8s.Wave(fleettest.KubeListNodes)
	k8s.Wave(fleettest.KubeListNodes, fleettest.KubeListPDBs, fleettest.KubeListDeployments, fleettest.KubeListNodeMetrics,
		fleettest.KubeListPods, fleettest.KubeListPods, fleettest.KubeListPods)
	k8s.Wave(fleettest.KubeListPods)
	k8s.Done()
	if err := <-done; err != nil {
		t.Fatalf("run: %v", err)
	}
}
