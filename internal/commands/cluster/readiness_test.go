package cluster

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"k8s.io/client-go/kubernetes"

	"github.com/dantech2000/refresh/internal/health"
	"github.com/dantech2000/refresh/internal/mocks"
)

// captureStderr collects what is written to os.Stderr while fn runs: the
// kube diagnostics resolveReadinessKubeClient prints go there.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	prev := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = w
	defer func() { os.Stderr = prev }()
	fn()
	_ = w.Close()
	var buf bytes.Buffer
	_, _ = io.Copy(&buf, r)
	return buf.String()
}

// readinessKubeconfig has one context, "other", whose server is not the
// target cluster.
func readinessKubeconfig(t *testing.T, server string) string {
	t.Helper()
	body := `apiVersion: v1
kind: Config
current-context: other
clusters:
- name: other
  cluster:
    server: ` + server + `
contexts:
- name: other
  context:
    cluster: other
    user: u
users:
- name: u
  user: {}
`
	path := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// When the cluster can't be described, node readiness degrades to "unknown":
// no client, and (for human output only) a diagnostic plus the readiness
// skip note.
func TestResolveReadinessKubeClient_DescribeFailure(t *testing.T) {
	api := mocks.NewEKSAPI().WithCluster("prod", "1.32").Build()
	kubeconfig := readinessKubeconfig(t, "https://example.invalid")

	for _, human := range []bool{true, false} {
		var client any
		out := captureStderr(t, func() {
			c, sel := resolveReadinessKubeClient(context.Background(), api, "us-east-1", "ghost", kubeconfig, "", human)
			client = c
			if sel.Target.Endpoint != "" {
				t.Errorf("target endpoint = %q for an undescribable cluster, want empty", sel.Target.Endpoint)
			}
		})
		if client != nil {
			t.Fatalf("human=%v: got a client for a cluster that could not be described", human)
		}
		if human {
			for _, want := range []string{"ghost", "Node readiness will show desired capacity only"} {
				if !strings.Contains(out, want) {
					t.Errorf("diagnostic %q should mention %q", out, want)
				}
			}
		} else if out != "" {
			t.Errorf("machine output must print no diagnostic, got %q", out)
		}
	}
}

// A kubeconfig whose only context points at another cluster is not used:
// readiness must never be measured on the wrong cluster. The selection still
// carries the described target, so callers can explain what was expected.
func TestResolveReadinessKubeClient_KubeconfigMismatch(t *testing.T) {
	api := mocks.NewEKSAPI().WithCluster("prod", "1.32", mocks.ClusterRegion("eu-west-1")).Build()
	kubeconfig := readinessKubeconfig(t, "https://0000000000000000000000000000AAAA.gr7.eu-west-1.eks.amazonaws.com")

	var gotClient bool
	var sel health.KubeSelection
	out := captureStderr(t, func() {
		var c kubernetes.Interface
		c, sel = resolveReadinessKubeClient(context.Background(), api, "eu-west-1", "prod", kubeconfig, "", false)
		gotClient = c != nil
	})
	if gotClient {
		t.Fatal("got a client for a kubeconfig that targets another cluster")
	}
	// The mismatch warning prints even for machine output: it explains why
	// readiness is partial.
	for _, want := range []string{"skipping Kubernetes checks", "aws eks update-kubeconfig --name prod --region eu-west-1", "Node readiness will show desired capacity only"} {
		if !strings.Contains(out, want) {
			t.Errorf("warning %q should mention %q", out, want)
		}
	}
	if sel.Target.Name != "prod" || !strings.Contains(sel.Target.Endpoint, ".gr7.eu-west-1.eks.amazonaws.com") || sel.Target.ARN == "" {
		t.Fatalf("target = %+v, want the described prod cluster", sel.Target)
	}
}

// A matching context whose API server is unreachable yields no client and,
// for human output, says why along with the skip note.
func TestResolveReadinessKubeClient_UnreachableAPI(t *testing.T) {
	// Port 1 on loopback refuses connections at once: no network, no wait.
	const endpoint = "https://127.0.0.1:1"
	api := mocks.NewEKSAPI().WithCluster("prod", "1.32", mocks.ClusterEndpoint(endpoint)).Build()
	kubeconfig := readinessKubeconfig(t, endpoint)

	var gotClient bool
	out := captureStderr(t, func() {
		c, _ := resolveReadinessKubeClient(context.Background(), api, "us-east-1", "prod", kubeconfig, "", true)
		gotClient = c != nil
	})
	if gotClient {
		t.Fatal("got a client for an unreachable API server")
	}
	if !strings.Contains(out, "Node readiness will show desired capacity only") {
		t.Errorf("diagnostic %q should carry the readiness skip note", out)
	}
}
