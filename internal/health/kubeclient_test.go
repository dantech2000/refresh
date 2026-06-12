package health

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	fakek8s "k8s.io/client-go/kubernetes/fake"
)

const testKubeconfig = `apiVersion: v1
kind: Config
current-context: test-ctx
clusters:
- name: c
  cluster:
    server: https://example.com
contexts:
- name: test-ctx
  context:
    cluster: c
    user: u
users:
- name: u
  user: {}
`

func TestBuildKubeClient_ExplicitPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config")
	if err := os.WriteFile(path, []byte(testKubeconfig), 0o600); err != nil {
		t.Fatal(err)
	}

	client, diag, err := BuildKubeClient(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if client == nil {
		t.Fatal("expected a client")
	}
	if diag.Source != "--kubeconfig" || diag.Path != path {
		t.Errorf("diag = %+v, want source=--kubeconfig path=%s", diag, path)
	}
	if diag.Context != "test-ctx" {
		t.Errorf("diag.Context = %q, want test-ctx", diag.Context)
	}
	if !strings.Contains(diag.String(), "test-ctx") {
		t.Errorf("diag.String() = %q, should name the context", diag.String())
	}
}

func TestBuildKubeClient_ExplicitMissingPathIsError(t *testing.T) {
	_, diag, err := BuildKubeClient("/no/such/kubeconfig")
	if err == nil {
		t.Fatal("expected an error for a missing explicit kubeconfig")
	}
	if !strings.Contains(err.Error(), "/no/such/kubeconfig") {
		t.Errorf("error %q should name the missing path", err)
	}
	if diag.Source != "--kubeconfig" {
		t.Errorf("diag.Source = %q, want --kubeconfig", diag.Source)
	}
}

func TestProbeConnection(t *testing.T) {
	if err := ProbeConnection(context.Background(), nil); err == nil {
		t.Error("nil client should error")
	}
	if err := ProbeConnection(context.Background(), fakek8s.NewSimpleClientset()); err != nil {
		t.Errorf("reachable fake client should succeed, got %v", err)
	}
}
