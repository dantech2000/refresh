package cluster_test

import (
	"encoding/json"
	"testing"

	"github.com/dantech2000/refresh/internal/apidoc"
	"github.com/dantech2000/refresh/internal/cmdutil"
	"github.com/dantech2000/refresh/internal/fleet/fleettest"
)

// "cluster list --show-health" and "cluster describe" give one cluster
// the same health summary. Neither connects to the cluster, so that the
// Kubernetes checks did not run is no warning in either. With
// --check-readiness and no kubeconfig context the user asked for the
// checks, and describe warns.
func TestHealthSummary_ListAndDescribeAgree(t *testing.T) {
	world := newWorld()
	world.Edit(func(s *fleettest.State) {
		s.Cluster(east, "prod-payments").Nodegroups = []*fleettest.Nodegroup{{Name: "web", Version: "1.33", Desired: 2}}
		s.Cluster(east, "prod-search").Nodegroups = []*fleettest.Nodegroup{{Name: "web", Version: "1.31", Desired: 2}}
		s.Metrics = append(controlPlaneMetrics("prod-payments", 7.7*(1<<30), 100, 0, 0), controlPlaneMetrics("prod-search", 1<<30, 100, 0, 0)...)
	})
	e := newEnv(t, world)
	if err := e.run(testContext(t), "--show-health", "-o", "json"); err != nil {
		t.Fatalf("cluster list: %v", err)
	}
	var list apidoc.ClusterList
	if err := json.Unmarshal(e.out.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Clusters) != 4 {
		t.Fatalf("cluster list read %d clusters, want 4", len(list.Clusters))
	}
	seen := map[apidoc.HealthOutcome]bool{}
	for _, c := range list.Clusters {
		d := newEnv(t, world)
		// A partial read exits 4 and still prints the document.
		if err := d.Run(testContext(t), "cluster", "describe", c.Name, "-o", "json"); err != nil && cmdutil.ExitCode(err) != cmdutil.ExitIncomplete {
			t.Fatalf("cluster describe %s: %v", c.Name, err)
		}
		var desc apidoc.ClusterDescription
		if err := json.Unmarshal(d.out.Bytes(), &desc); err != nil {
			t.Fatal(err)
		}
		if c.Health == nil || desc.Cluster.Health == nil {
			t.Fatalf("%s: health of list %v, of describe %v, want both", c.Name, c.Health, desc.Cluster.Health)
		}
		if c.Health.Summary != desc.Cluster.Health.Summary {
			t.Errorf("%s: cluster list says %s and cluster describe says %s", c.Name, c.Health.Summary, desc.Cluster.Health.Summary)
		}
		seen[c.Health.Summary] = true
	}
	if !seen[apidoc.HealthPass] || !seen[apidoc.HealthFail] {
		t.Errorf("the summaries are %v, want a Pass and a Fail among them", seen)
	}

	// The user asked for the Kubernetes checks and there is no context
	// for the cluster: that is a warning.
	d := newEnv(t, world)
	_ = d.Run(testContext(t), "cluster", "describe", "prod-search", "--check-readiness", "-o", "json")
	var desc apidoc.ClusterDescription
	if err := json.Unmarshal(d.out.Bytes(), &desc); err != nil {
		t.Fatal(err)
	}
	if desc.Cluster.Health == nil || desc.Cluster.Health.Summary != apidoc.HealthWarn {
		t.Errorf("--check-readiness without Kubernetes access: health = %+v, want the summary Warn", desc.Cluster.Health)
	}
}
