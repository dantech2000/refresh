package runner

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"
	"k8s.io/client-go/kubernetes"

	"github.com/dantech2000/refresh/internal/mocks"
)

const (
	testProdEndpoint    = "https://aaa111.gr7.us-east-1.eks.amazonaws.com"
	testStagingEndpoint = "https://bbb222.gr7.us-east-1.eks.amazonaws.com"
	testOtherEndpoint   = "https://ccc333.gr7.us-east-1.eks.amazonaws.com"
)

// stagingKubeconfig mimics kubectl pointed at staging, with a prod context
// also present.
const stagingKubeconfig = `apiVersion: v1
kind: Config
current-context: staging
clusters:
- name: staging
  cluster:
    server: ` + testStagingEndpoint + `
- name: prod
  cluster:
    server: ` + testProdEndpoint + `
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

func eksWithEndpoint(endpoint string) *mocks.EKSAPI {
	api := mocks.NewEKSAPI().Build()
	api.DescribeClusterFn = func(_ context.Context, in *eks.DescribeClusterInput, _ ...func(*eks.Options)) (*eks.DescribeClusterOutput, error) {
		return &eks.DescribeClusterOutput{Cluster: &ekstypes.Cluster{Name: in.Name, Endpoint: aws.String(endpoint)}}, nil
	}
	return api
}

// stubKubeSeams captures warnings and replaces the connectivity probe so no
// live API server is needed.
func stubKubeSeams(t *testing.T) (*bytes.Buffer, *int) {
	t.Helper()
	var buf bytes.Buffer
	probes := 0
	prevOut, prevProbe := kubeWarnOut, probeKube
	kubeWarnOut = &buf
	probeKube = func(context.Context, kubernetes.Interface) error { probes++; return nil }
	t.Cleanup(func() { kubeWarnOut, probeKube = prevOut, prevProbe })
	return &buf, &probes
}

func writeTestKubeconfig(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(path, []byte(stagingKubeconfig), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestResolveClusterKubeClient_MismatchSkipsWithWarning(t *testing.T) {
	out, probes := stubKubeSeams(t)
	client, _ := ResolveClusterKubeClient(context.Background(), KubeRequest{
		API:        eksWithEndpoint(testOtherEndpoint),
		Cluster:    "mismatch-prod",
		Region:     "us-east-1",
		Kubeconfig: writeTestKubeconfig(t),
		SkipNote:   "Workload/PDB checks will be skipped.",
	})
	if client != nil {
		t.Fatal("expected no client when the kubeconfig does not point at the target cluster")
	}
	if *probes != 0 {
		t.Errorf("probe ran %d time(s); a mismatched client must not be used", *probes)
	}
	msg := out.String()
	for _, want := range []string{
		`"staging"`, testStagingEndpoint, // the kube context's server
		"mismatch-prod", testOtherEndpoint, // the target cluster
		"aws eks update-kubeconfig --name mismatch-prod --region us-east-1",
		"Workload/PDB checks will be skipped.",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("warning %q should mention %q", msg, want)
		}
	}

	// A second resolution for the same cluster in one run doesn't repeat it.
	out.Reset()
	_, _ = ResolveClusterKubeClient(context.Background(), KubeRequest{
		API: eksWithEndpoint(testOtherEndpoint), Cluster: "mismatch-prod", Region: "us-east-1",
		Kubeconfig: writeTestKubeconfig(t),
	})
	if out.Len() != 0 {
		t.Errorf("repeated mismatch warning: %q", out.String())
	}
}

func TestResolveClusterKubeClient_SelectsMatchingContext(t *testing.T) {
	out, probes := stubKubeSeams(t)
	client, target := ResolveClusterKubeClient(context.Background(), KubeRequest{
		API:        eksWithEndpoint(testProdEndpoint),
		Cluster:    "switch-prod",
		Region:     "us-east-1",
		Kubeconfig: writeTestKubeconfig(t),
		Verbose:    true,
	})
	if client == nil {
		t.Fatal("expected a client from the prod context")
	}
	if *probes != 1 {
		t.Errorf("probes = %d, want 1", *probes)
	}
	if target.Endpoint != testProdEndpoint {
		t.Errorf("target endpoint = %q, want %q", target.Endpoint, testProdEndpoint)
	}
	if !strings.Contains(out.String(), `context "prod"`) {
		t.Errorf("expected a context-switch notice, got %q", out.String())
	}
}

func TestResolveClusterKubeClient_CurrentContextMatches(t *testing.T) {
	out, _ := stubKubeSeams(t)
	client, _ := ResolveClusterKubeClient(context.Background(), KubeRequest{
		API:        eksWithEndpoint(testStagingEndpoint + "/"),
		Cluster:    "current-staging",
		Region:     "us-east-1",
		Kubeconfig: writeTestKubeconfig(t),
		Verbose:    true,
	})
	if client == nil {
		t.Fatal("expected a client from the current context")
	}
	if out.Len() != 0 {
		t.Errorf("unexpected notice: %q", out.String())
	}
}
