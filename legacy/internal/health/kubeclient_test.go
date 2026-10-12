package health

import (
	"context"
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

func TestConnect_ExplicitPath(t *testing.T) {
	path := writeKubeconfig(t, testKubeconfig)
	target := TargetCluster{Name: "c", Endpoint: "https://example.com"}

	client, sel, err := ConnectKubeClientForCluster(context.Background(), path, "", target, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if client == nil {
		t.Fatal("expected a client")
	}
	diag := sel.Diag
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

func TestConnect_ExplicitMissingPathIsError(t *testing.T) {
	_, sel, err := ConnectKubeClientForCluster(context.Background(), "/no/such/kubeconfig", "", prodTarget, nil)
	if err == nil {
		t.Fatal("expected an error for a missing explicit kubeconfig")
	}
	if !strings.Contains(err.Error(), "/no/such/kubeconfig") {
		t.Errorf("error %q should name the missing path", err)
	}
	if sel.Diag.Source != "--kubeconfig" {
		t.Errorf("diag.Source = %q, want --kubeconfig", sel.Diag.Source)
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
