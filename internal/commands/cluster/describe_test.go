package cluster

import (
	"strings"
	"testing"

	"github.com/dantech2000/refresh/internal/mocks/fakeaws"
)

func describeWorld() *fakeaws.Cluster {
	return &fakeaws.Cluster{Name: "prod", Version: "1.32", Nodegroups: []*fakeaws.Nodegroup{{Name: "web", Version: "1.32"}}}
}

// --check-readiness alone shows the nodegroups whose readiness it measures;
// before, they were read only with --detailed (REF-168).
func TestDescribe_CheckReadinessShowsNodegroups(t *testing.T) {
	fakeaws.New(t, describeWorld())
	stdout, stderr, err := runCluster(t, "describe", "prod", "--check-readiness", "--no-health", "-o", "json")
	if err != nil {
		t.Fatalf("describe --check-readiness: %v\nstderr:\n%s", err, stderr)
	}
	doc := fakeaws.RequireOneDocument(t, "json", stdout).(map[string]any)
	ngs, ok := doc["nodegroups"].([]any)
	if !ok || len(ngs) != 1 || ngs[0].(map[string]any)["name"] != "web" {
		t.Fatalf("nodegroups = %v, want [web]", doc["nodegroups"])
	}
	// The fake has no Kubernetes API, so readiness is honestly unknown.
	if ngs[0].(map[string]any)["readyKnown"] != false {
		t.Errorf("readyKnown = %v, want false", ngs[0].(map[string]any)["readyKnown"])
	}

	fakeaws.New(t, describeWorld())
	stdout, _, err = runCluster(t, "describe", "prod", "--check-readiness", "--no-health")
	if err != nil {
		t.Fatalf("describe --check-readiness (table): %v", err)
	}
	if !strings.Contains(stdout, "NODEGROUPS") || !strings.Contains(stdout, "web") {
		t.Errorf("table has no NODEGROUPS section:\n%s", stdout)
	}
}

// --show-security adds the SECURITY section to the table and leaves the
// JSON document as it was (REF-168).
func TestDescribe_ShowSecurity(t *testing.T) {
	fakeaws.New(t, describeWorld())
	plainJSON, _, err := runCluster(t, "describe", "prod", "--no-health", "-o", "json")
	if err != nil {
		t.Fatalf("describe -o json: %v", err)
	}
	fakeaws.New(t, describeWorld())
	secJSON, _, err := runCluster(t, "describe", "prod", "--no-health", "--show-security", "-o", "json")
	if err != nil {
		t.Fatalf("describe --show-security -o json: %v", err)
	}
	if plainJSON != secJSON {
		t.Errorf("--show-security changed the JSON document:\nwithout:\n%s\nwith:\n%s", plainJSON, secJSON)
	}

	fakeaws.New(t, describeWorld())
	table, _, err := runCluster(t, "describe", "prod", "--no-health")
	if err != nil {
		t.Fatalf("describe: %v", err)
	}
	if strings.Contains(table, "SECURITY") {
		t.Errorf("SECURITY section without --show-security:\n%s", table)
	}
	fakeaws.New(t, describeWorld())
	table, _, err = runCluster(t, "describe", "prod", "--no-health", "--show-security")
	if err != nil {
		t.Fatalf("describe --show-security: %v", err)
	}
	if !strings.Contains(table, "SECURITY") || !strings.Contains(table, "service role") {
		t.Errorf("--show-security table has no SECURITY section:\n%s", table)
	}
}
