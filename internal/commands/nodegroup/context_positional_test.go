package nodegroup

import (
	"testing"

	"github.com/dantech2000/refresh/internal/cliconfig"
	"github.com/dantech2000/refresh/internal/mocks/fakeaws"
)

// With an active context, `nodegroup describe <nodegroup>` describes the
// nodegroup in the context's cluster. The lone positional used to fill the
// cluster slot, so the command failed with "missing nodegroup name". The
// documented `nodegroup describe <cluster> <nodegroup>` form still names the
// cluster, even when a context is active.
func TestDescribe_LonePositionalIsNodegroupWithContext(t *testing.T) {
	fakeaws.New(t,
		prodCluster(&fakeaws.Nodegroup{Name: "api", Version: "1.31"}),
		&fakeaws.Cluster{Name: "dev", Version: "1.31", Nodegroups: []*fakeaws.Nodegroup{{Name: "api", Version: "1.31", Status: "DEGRADED"}}},
	)
	f := &cliconfig.File{Contexts: map[string]cliconfig.Context{}}
	if err := f.Set("p", cliconfig.Context{Cluster: "prod"}); err != nil {
		t.Fatal(err)
	}
	if err := f.Use("p"); err != nil {
		t.Fatal(err)
	}
	if err := cliconfig.Save(f); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		args       []string
		wantStatus string
	}{
		{[]string{"api"}, "ACTIVE"},
		{[]string{"dev", "api"}, "DEGRADED"},
		{[]string{"-c", "dev", "api"}, "DEGRADED"},
		{[]string{"-n", "api"}, "ACTIVE"},
		{[]string{"-n", "api", "dev"}, "DEGRADED"},
	} {
		args := append([]string{"describe"}, tc.args...)
		stdout, stderr, err := runNodegroup(t, append(args, "-o", "json")...)
		if err != nil {
			t.Errorf("%v: %v\nstderr:\n%s", tc.args, err, stderr)
			continue
		}
		doc := fakeaws.RequireOneDocument(t, "json", stdout).(map[string]any)
		if doc["name"] != "api" || doc["status"] != tc.wantStatus {
			t.Errorf("%v: got %v %v, want api %s", tc.args, doc["name"], doc["status"], tc.wantStatus)
		}
	}
}
