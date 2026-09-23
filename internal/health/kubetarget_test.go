package health

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/dantech2000/refresh/internal/mocks"
)

const (
	prodEndpoint    = "https://ABC123.gr7.us-east-1.eks.amazonaws.com"
	stagingEndpoint = "https://DEF456.gr7.us-west-2.eks.amazonaws.com"
)

func TestSameClusterEndpoint(t *testing.T) {
	cases := []struct {
		name             string
		server, endpoint string
		want             bool
	}{
		{"identical", prodEndpoint, prodEndpoint, true},
		{"trailing slash", prodEndpoint + "/", prodEndpoint, true},
		{"case", strings.ToLower(prodEndpoint), prodEndpoint, true},
		{"explicit default port", "https://abc123.gr7.us-east-1.eks.amazonaws.com:443", prodEndpoint, true},
		{"no scheme", "abc123.gr7.us-east-1.eks.amazonaws.com", prodEndpoint, true},
		{"trailing dot", "https://abc123.gr7.us-east-1.eks.amazonaws.com./", prodEndpoint, true},
		{"surrounding space", "  " + prodEndpoint + " ", prodEndpoint, true},
		{"other cluster", stagingEndpoint, prodEndpoint, false},
		{"other port", "https://abc123.gr7.us-east-1.eks.amazonaws.com:6443", prodEndpoint, false},
		{"empty server", "", prodEndpoint, false},
		{"empty endpoint", prodEndpoint, "", false},
		{"both empty", "", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := SameClusterEndpoint(tc.server, tc.endpoint); got != tc.want {
				t.Errorf("SameClusterEndpoint(%q, %q) = %v, want %v", tc.server, tc.endpoint, got, tc.want)
			}
		})
	}
}

// multiClusterKubeconfig has "staging" as the current context and two
// contexts ("prod-a", "prod-b") that both point at prod, e.g. two users.
const multiClusterKubeconfig = `apiVersion: v1
kind: Config
current-context: staging
clusters:
- name: staging
  cluster:
    server: ` + stagingEndpoint + `
- name: prod
  cluster:
    server: ` + prodEndpoint + `/
contexts:
- name: staging
  context:
    cluster: staging
    user: u
- name: prod-b
  context:
    cluster: prod
    user: u
- name: prod-a
  context:
    cluster: prod
    user: u
users:
- name: u
  user: {}
`

var (
	prodTarget    = TargetCluster{Name: "prod", Region: "us-east-1", Endpoint: prodEndpoint}
	stagingTarget = TargetCluster{Name: "staging", Region: "us-west-2", Endpoint: stagingEndpoint}
)

func writeKubeconfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func connect(t *testing.T, path, kubeContext string, target TargetCluster, probe ProbeFunc) (kubernetes.Interface, KubeSelection, error) {
	t.Helper()
	return ConnectKubeClientForCluster(context.Background(), path, kubeContext, target, probe)
}

func TestConnect_CurrentContextMatches(t *testing.T) {
	client, sel, err := connect(t, writeKubeconfig(t, multiClusterKubeconfig), "", stagingTarget, nil)
	if err != nil || client == nil {
		t.Fatalf("Connect() = %v, %v", client, err)
	}
	if sel.Diag.Context != "staging" || sel.Diag.SwitchedFrom != "" || sel.Diag.Unverified {
		t.Errorf("diag = %+v, want context staging, no switch", sel.Diag)
	}
}

func TestConnect_SelectsMatchingContext(t *testing.T) {
	_, sel, err := connect(t, writeKubeconfig(t, multiClusterKubeconfig), "", prodTarget, nil)
	if err != nil {
		t.Fatal(err)
	}
	if sel.Diag.Context != "prod-a" || sel.Diag.SwitchedFrom != "staging" {
		t.Errorf("diag = %+v, want context prod-a switched from staging", sel.Diag)
	}
	if !SameClusterEndpoint(sel.config.Host, prodEndpoint) {
		t.Errorf("selected host = %q, want the prod endpoint", sel.config.Host)
	}
	if _, err := BuildMetricsClient(sel); err != nil {
		t.Errorf("BuildMetricsClient() error: %v", err)
	}
}

func TestConnect_TriesMatchingContextsUntilProbeSucceeds(t *testing.T) {
	path := writeKubeconfig(t, multiClusterKubeconfig)
	calls := 0
	failFirst := func(context.Context, kubernetes.Interface) error {
		calls++
		if calls == 1 {
			return errors.New("connection refused")
		}
		return nil
	}
	client, sel, err := connect(t, path, "", prodTarget, failFirst)
	if err != nil || client == nil {
		t.Fatalf("Connect() = %v, %v", client, err)
	}
	if sel.Diag.Context != "prod-b" || calls != 2 {
		t.Errorf("context = %q after %d probe(s), want prod-b after 2", sel.Diag.Context, calls)
	}

	allFail := func(context.Context, kubernetes.Interface) error { return errors.New("timeout") }
	if client, _, err := connect(t, path, "", prodTarget, allFail); client != nil || !IsProbeError(err) {
		t.Errorf("all probes failing: client=%v err=%v, want nil client and a probe error", client, err)
	}
}

func TestConnect_NoMatchingContext(t *testing.T) {
	path := writeKubeconfig(t, testKubeconfig) // single context -> https://example.com
	client, sel, err := connect(t, path, "", prodTarget, nil)
	if client != nil {
		t.Fatal("expected no client when no context points at the target cluster")
	}
	var mm *ClusterMismatchError
	if !errors.As(err, &mm) {
		t.Fatalf("error = %v, want *ClusterMismatchError", err)
	}
	msg := err.Error()
	for _, want := range []string{"test-ctx", "https://example.com", "prod", prodEndpoint} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q should mention %q", msg, want)
		}
	}
	if got, want := mm.Target.UpdateKubeconfigHint(), "aws eks update-kubeconfig --name prod --region us-east-1"; got != want {
		t.Errorf("hint = %q, want %q", got, want)
	}
	if _, err := BuildMetricsClient(sel); err == nil {
		t.Error("BuildMetricsClient must refuse an unverified selection")
	}
}

func TestConnect_NoEndpointIsMismatch(t *testing.T) {
	_, _, err := connect(t, writeKubeconfig(t, multiClusterKubeconfig), "", TargetCluster{Name: "prod"}, nil)
	var mm *ClusterMismatchError
	if !errors.As(err, &mm) {
		t.Fatalf("error = %v, want *ClusterMismatchError", err)
	}
}

func TestConnect_ExplicitContext(t *testing.T) {
	path := writeKubeconfig(t, multiClusterKubeconfig)

	// A named context that doesn't match (e.g. a tunnel) is trusted, flagged.
	client, sel, err := connect(t, path, "staging", prodTarget, nil)
	if err != nil || client == nil {
		t.Fatalf("Connect(--kube-context staging) = %v, %v", client, err)
	}
	if sel.Diag.Context != "staging" || !sel.Diag.Unverified {
		t.Errorf("diag = %+v, want context staging, Unverified", sel.Diag)
	}

	// A named context that matches is not flagged.
	if _, sel, err := connect(t, path, "prod-b", prodTarget, nil); err != nil || sel.Diag.Unverified || sel.Diag.Context != "prod-b" {
		t.Errorf("Connect(--kube-context prod-b) diag=%+v err=%v, want verified prod-b", sel.Diag, err)
	}

	// An unknown context is an error, not a silent fallback.
	if _, _, err := connect(t, path, "nope", prodTarget, nil); err == nil || !strings.Contains(err.Error(), `"nope"`) {
		t.Errorf("unknown context error = %v", err)
	}
}

func TestConnect_InCluster(t *testing.T) {
	t.Setenv("KUBECONFIG", filepath.Join(t.TempDir(), "missing"))
	prev := inClusterConfig
	inClusterConfig = func() (*rest.Config, error) { return &rest.Config{Host: "https://10.100.0.1:443"}, nil }
	t.Cleanup(func() { inClusterConfig = prev })

	// Unverifiable by default: skipped, with the env var named in the error.
	t.Setenv(InClusterNameEnv, "")
	client, _, err := connect(t, "", "", prodTarget, nil)
	var mm *ClusterMismatchError
	if client != nil || !errors.As(err, &mm) || !mm.InCluster {
		t.Fatalf("in-cluster without %s: client=%v err=%v, want in-cluster mismatch", InClusterNameEnv, client, err)
	}
	if !strings.Contains(err.Error(), InClusterNameEnv+"=prod") {
		t.Errorf("error %q should name %s=prod", err, InClusterNameEnv)
	}

	// Declared for another cluster: still skipped.
	t.Setenv(InClusterNameEnv, "staging")
	if client, _, err := connect(t, "", "", prodTarget, nil); client != nil || !errors.As(err, &mm) {
		t.Errorf("in-cluster declared for staging: client=%v err=%v, want mismatch", client, err)
	}

	// Declared for the target cluster: trusted.
	t.Setenv(InClusterNameEnv, "prod")
	client, sel, err := connect(t, "", "", prodTarget, nil)
	if err != nil || client == nil || sel.Diag.Source != "in-cluster" {
		t.Errorf("in-cluster declared for prod: client=%v diag=%+v err=%v, want in-cluster client", client, sel.Diag, err)
	}
}

func TestDescribeTarget(t *testing.T) {
	api := mocks.NewEKSAPI().Build()
	api.DescribeClusterFn = func(_ context.Context, in *eks.DescribeClusterInput, _ ...func(*eks.Options)) (*eks.DescribeClusterOutput, error) {
		return &eks.DescribeClusterOutput{Cluster: &ekstypes.Cluster{
			Name:     in.Name,
			Arn:      aws.String("arn:aws:eks:us-east-1:111122223333:cluster/prod"),
			Endpoint: aws.String(prodEndpoint),
		}}, nil
	}
	got, err := DescribeTarget(context.Background(), api, "prod", "us-east-1")
	if err != nil {
		t.Fatal(err)
	}
	want := TargetCluster{Name: "prod", Region: "us-east-1", ARN: "arn:aws:eks:us-east-1:111122223333:cluster/prod", Endpoint: prodEndpoint}
	if got != want {
		t.Errorf("DescribeTarget() = %+v, want %+v", got, want)
	}
}
