package cmdutil_test

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/dantech2000/refresh/internal/cmdutil"
	"github.com/dantech2000/refresh/internal/cmdutil/cmdutiltest"
	"github.com/dantech2000/refresh/internal/kube"
)

// writeKubeconfig writes a kubeconfig with one context for each name, to
// the server of the name.
func writeKubeconfig(t *testing.T, path string, servers map[string]string) {
	t.Helper()
	clusters, contexts := "clusters:\n", "contexts:\n"
	for name, server := range servers {
		clusters += fmt.Sprintf("- name: %s\n  cluster:\n    server: %s\n", name, server)
		contexts += fmt.Sprintf("- name: %s\n  context:\n    cluster: %s\n    user: fake\n", name, name)
	}
	data := "apiVersion: v1\nkind: Config\n" + clusters + contexts + "users:\n- name: fake\n  user:\n    token: fake-token\n"
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
}

// The client of a target is kept for the whole target: two clusters of
// one name in two regions get their own client.
func TestNewKubeKeysOnTheTarget(t *testing.T) {
	t.Setenv(cmdutil.EnvInClusterName, "")
	path := filepath.Join(t.TempDir(), "kubeconfig")
	writeKubeconfig(t, path, map[string]string{"east": "https://east.example", "west": "https://west.example"})
	f, _, _, _ := cmdutiltest.NewFactory()
	f.Global.Kubeconfig = path
	connect := cmdutil.NewKube(f)

	east := kube.Target{Name: "prod", Region: "us-east-1", Endpoint: "https://east.example"}
	west := kube.Target{Name: "prod", Region: "eu-west-1", Endpoint: "https://west.example"}
	for target, want := range map[kube.Target]string{east: "east", west: "west"} {
		client, err := connect(target)
		if err != nil {
			t.Fatalf("%v: %v", target, err)
		}
		if client.Selection.Context != want {
			t.Errorf("%s in %s got the context %q, want %q", target.Name, target.Region, client.Selection.Context, want)
		}
		again, err := connect(target)
		if err != nil || again != client {
			t.Errorf("%s in %s: a second call gave another client (%v)", target.Name, target.Region, err)
		}
	}
}

// A target that could not be resolved is resolved again on the next call,
// so the UI sees a kubeconfig context that the user adds while it runs.
func TestNewKubeKeepsNoFailure(t *testing.T) {
	t.Setenv(cmdutil.EnvInClusterName, "")
	path := filepath.Join(t.TempDir(), "kubeconfig")
	writeKubeconfig(t, path, map[string]string{"east": "https://east.example"})
	f, _, _, _ := cmdutiltest.NewFactory()
	f.Global.Kubeconfig = path
	connect := cmdutil.NewKube(f)

	west := kube.Target{Name: "prod", Region: "eu-west-1", Endpoint: "https://west.example"}
	if _, err := connect(west); err == nil {
		t.Fatal("no error for a target that no context reaches")
	}
	writeKubeconfig(t, path, map[string]string{"east": "https://east.example", "west": "https://west.example"})
	client, err := connect(west)
	if err != nil {
		t.Fatalf("after the context was added: %v", err)
	}
	if client.Selection.Context != "west" {
		t.Errorf("context = %q, want west", client.Selection.Context)
	}
}
