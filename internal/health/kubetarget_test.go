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

// twoClusterKubeconfig has "staging" as the current context and a "prod"
// context, like a kubeconfig after `aws eks update-kubeconfig` for both.
const twoClusterKubeconfig = `apiVersion: v1
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
- name: prod
  context:
    cluster: prod
    user: u
users:
- name: u
  user: {}
`

func writeKubeconfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestBuildKubeClientForCluster_CurrentContextMatches(t *testing.T) {
	path := writeKubeconfig(t, twoClusterKubeconfig)
	client, diag, err := BuildKubeClientForCluster(path, TargetCluster{Name: "staging", Endpoint: stagingEndpoint})
	if err != nil || client == nil {
		t.Fatalf("BuildKubeClientForCluster() = %v, %v", client, err)
	}
	if diag.Context != "staging" || diag.SwitchedFrom != "" {
		t.Errorf("diag = %+v, want context staging, no switch", diag)
	}
}

func TestBuildKubeClientForCluster_SelectsMatchingContext(t *testing.T) {
	path := writeKubeconfig(t, twoClusterKubeconfig)
	cfg, diag, err := resolveRESTConfig(path, &TargetCluster{Name: "prod", Endpoint: prodEndpoint})
	if err != nil {
		t.Fatalf("resolveRESTConfig() error: %v", err)
	}
	if diag.Context != "prod" || diag.SwitchedFrom != "staging" {
		t.Errorf("diag = %+v, want context prod switched from staging", diag)
	}
	if !SameClusterEndpoint(cfg.Host, prodEndpoint) {
		t.Errorf("rest config host = %q, want the prod endpoint", cfg.Host)
	}
}

func TestBuildKubeClientForCluster_NoMatchingContext(t *testing.T) {
	path := writeKubeconfig(t, testKubeconfig) // single context -> https://example.com
	target := TargetCluster{Name: "prod", Region: "us-east-1", Endpoint: prodEndpoint}
	client, _, err := BuildKubeClientForCluster(path, target)
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

	// The metrics client must refuse the same way.
	if _, err := BuildMetricsClientForCluster(path, target); !errors.As(err, &mm) {
		t.Errorf("BuildMetricsClientForCluster error = %v, want *ClusterMismatchError", err)
	}
}

func TestBuildKubeClientForCluster_NoEndpointIsMismatch(t *testing.T) {
	path := writeKubeconfig(t, twoClusterKubeconfig)
	_, _, err := BuildKubeClientForCluster(path, TargetCluster{Name: "prod"})
	var mm *ClusterMismatchError
	if !errors.As(err, &mm) {
		t.Fatalf("error = %v, want *ClusterMismatchError", err)
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

func TestResolveRESTConfig_InClusterIsUsedButUnverified(t *testing.T) {
	t.Setenv("KUBECONFIG", filepath.Join(t.TempDir(), "missing"))
	prev := inClusterConfig
	inClusterConfig = func() (*rest.Config, error) { return &rest.Config{Host: "https://10.100.0.1:443"}, nil }
	t.Cleanup(func() { inClusterConfig = prev })

	cfg, diag, err := resolveRESTConfig("", &TargetCluster{Name: "prod", Endpoint: prodEndpoint})
	if err != nil || cfg == nil {
		t.Fatalf("resolveRESTConfig() = %v, %v; in-cluster config should be used", cfg, err)
	}
	if diag.Source != "in-cluster" || !diag.Unverified {
		t.Errorf("diag = %+v, want in-cluster and Unverified", diag)
	}

	// Without a target there is nothing to verify.
	if _, diag, _ := resolveRESTConfig("", nil); diag.Unverified {
		t.Errorf("diag = %+v, want Unverified=false without a target", diag)
	}
}
