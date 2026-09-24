package addon

import (
	"testing"

	"github.com/dantech2000/refresh/internal/cliconfig"
	"github.com/dantech2000/refresh/internal/mocks/fakeaws"
)

// useContext saves a context named name for cluster and makes it current, in
// the context store fakeaws.New pointed at a temp directory.
func useContext(t *testing.T, name, cluster string) {
	t.Helper()
	f := &cliconfig.File{Contexts: map[string]cliconfig.Context{}}
	if err := f.Set(name, cliconfig.Context{Cluster: cluster}); err != nil {
		t.Fatal(err)
	}
	if err := f.Use(name); err != nil {
		t.Fatal(err)
	}
	if err := cliconfig.Save(f); err != nil {
		t.Fatal(err)
	}
}

// With an active context, `addon describe <addon>` describes the add-on in
// the context's cluster. The lone positional used to be taken as the
// cluster. The documented `addon describe <cluster> <addon>` form still
// names the cluster, even when a context is active.
func TestDescribe_LonePositionalIsAddonWithContext(t *testing.T) {
	fakeaws.New(t,
		addonCluster(&fakeaws.Addon{Name: "coredns", Version: "v1.11.4"}),
		&fakeaws.Cluster{Name: "dev", Version: "1.31", Addons: []*fakeaws.Addon{{Name: "coredns", Version: "v1.10.0"}}},
	)
	useContext(t, "p", "prod")

	for _, tc := range []struct {
		args        []string
		wantVersion string
	}{
		{[]string{"coredns"}, "v1.11.4"},
		{[]string{"dev", "coredns"}, "v1.10.0"},
		{[]string{"-c", "dev", "coredns"}, "v1.10.0"},
		{[]string{"-a", "coredns"}, "v1.11.4"},
		{[]string{"-a", "coredns", "dev"}, "v1.10.0"},
	} {
		args := append([]string{"describe"}, tc.args...)
		stdout, stderr, err := runAddon(t, append(args, "-o", "json")...)
		if err != nil {
			t.Errorf("%v: %v\nstderr:\n%s", tc.args, err, stderr)
			continue
		}
		doc := fakeaws.RequireOneDocument(t, "json", stdout).(map[string]any)
		if doc["name"] != "coredns" || doc["version"] != tc.wantVersion {
			t.Errorf("%v: got %v %v, want coredns %s", tc.args, doc["name"], doc["version"], tc.wantVersion)
		}
	}
}
