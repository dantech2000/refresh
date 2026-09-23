package aws

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/eks"

	"github.com/dantech2000/refresh/internal/mocks"
)

// withTTY forces stdinIsTerminal for the duration of the test.
func withTTY(t *testing.T, tty bool) {
	t.Helper()
	orig := stdinIsTerminal
	stdinIsTerminal = func() bool { return tty }
	t.Cleanup(func() { stdinIsTerminal = orig })
}

// isolateConfig points the refresh context store and kubeconfig at empty
// temp locations so the developer's own setup never leaks into a test.
func isolateConfig(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("REFRESH_CONFIG_HOME", dir)
	t.Setenv("REFRESH_CONTEXT", "")
	t.Setenv("KUBECONFIG", filepath.Join(dir, "missing-kubeconfig"))
	return dir
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func clustersAPI(names ...string) *mocks.EKSAPI {
	m := mocks.NewEKSAPI().Build()
	m.ListClustersFn = func(_ context.Context, _ *eks.ListClustersInput, _ ...func(*eks.Options)) (*eks.ListClustersOutput, error) {
		return &eks.ListClustersOutput{Clusters: names}, nil
	}
	return m
}

func TestMatchingClusters_ExactNamePreferredOverSubstrings(t *testing.T) {
	got := MatchingClusters([]string{"prod-legacy", "prod", "prod-2"}, "prod")
	if len(got) != 1 || got[0] != "prod" {
		t.Errorf("exact match should win alone, got %v", got)
	}
}

func TestMatchingNodegroups_ExactNamePreferredOverSubstrings(t *testing.T) {
	got := MatchingNodegroups([]string{"ng-a", "ng-a-spot", "ng-a2"}, "ng-a")
	if len(got) != 1 || got[0] != "ng-a" {
		t.Errorf("exact match should win alone, got %v", got)
	}
}

func TestResolveClusterName_ExactMatchWithSubstringSiblings(t *testing.T) {
	withTTY(t, false)
	got, err := resolveClusterName(context.Background(), clustersAPI("prod-legacy", "prod"), "prod", ClusterNameOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "prod" {
		t.Errorf("got %q, want prod", got)
	}
}

func TestResolveClusterName_SingleSubstringMatchMutatingNoTTYErrors(t *testing.T) {
	withTTY(t, false)
	_, err := resolveClusterName(context.Background(), clustersAPI("prod-legacy", "staging"), "prod", ClusterNameOptions{})
	if err == nil {
		t.Fatal("expected an error: -c prod must not silently resolve to prod-legacy for a mutating command")
	}
	if !strings.Contains(err.Error(), "prod-legacy") {
		t.Errorf("error should name the candidate, got %v", err)
	}
}

func TestResolveClusterName_SingleSubstringMatchReadOnlyNoTTYAccepts(t *testing.T) {
	withTTY(t, false)
	got, err := resolveClusterName(context.Background(), clustersAPI("prod-legacy", "staging"), "prod", ClusterNameOptions{ReadOnly: true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "prod-legacy" {
		t.Errorf("got %q, want prod-legacy", got)
	}
}

func TestResolveClusterName_MultipleMatchesNoTTYErrors(t *testing.T) {
	withTTY(t, false)
	_, err := resolveClusterName(context.Background(), clustersAPI("prod-east", "prod-west"), "prod", ClusterNameOptions{ReadOnly: true})
	if err == nil {
		t.Fatal("expected an error for an ambiguous pattern without a TTY")
	}
	if !strings.Contains(err.Error(), "prod-east") || !strings.Contains(err.Error(), "prod-west") {
		t.Errorf("error should list candidates, got %v", err)
	}
}

func TestResolveClusterName_NoMatchErrors(t *testing.T) {
	withTTY(t, false)
	_, err := resolveClusterName(context.Background(), clustersAPI("staging"), "prod", ClusterNameOptions{ReadOnly: true})
	if err == nil {
		t.Fatal("expected an error for no match")
	}
}

func TestResolveClusterPattern_FlagWins(t *testing.T) {
	dir := isolateConfig(t)
	writeFile(t, filepath.Join(dir, "context.yaml"), "current: p\ncontexts:\n  p:\n    cluster: from-context\n")
	got, err := resolveClusterPattern("from-flag")
	if err != nil || got != "from-flag" {
		t.Errorf("got %q, %v; want from-flag", got, err)
	}
}

func TestResolveClusterPattern_ActiveContextBeforeKubeconfig(t *testing.T) {
	dir := isolateConfig(t)
	writeFile(t, filepath.Join(dir, "context.yaml"), "current: p\ncontexts:\n  p:\n    cluster: from-context\n")
	kc := filepath.Join(dir, "kubeconfig")
	writeFile(t, kc, kubeconfigFor("from-kubeconfig"))
	t.Setenv("KUBECONFIG", kc)

	got, err := resolveClusterPattern("")
	if err != nil || got != "from-context" {
		t.Errorf("got %q, %v; want from-context", got, err)
	}
}

func TestResolveClusterPattern_KubeconfigFallback(t *testing.T) {
	dir := isolateConfig(t)
	kc := filepath.Join(dir, "kubeconfig")
	writeFile(t, kc, kubeconfigFor("arn:aws:eks:us-east-1:123456789012:cluster/from-kubeconfig"))
	t.Setenv("KUBECONFIG", kc)

	got, err := resolveClusterPattern("")
	if err != nil || got != "from-kubeconfig" {
		t.Errorf("got %q, %v; want from-kubeconfig", got, err)
	}
}

func TestResolveClusterPattern_NothingResolvesWrapsSentinel(t *testing.T) {
	isolateConfig(t)
	_, err := resolveClusterPattern("")
	if !errors.Is(err, ErrNoClusterSpecified) {
		t.Errorf("want ErrNoClusterSpecified, got %v", err)
	}
}

func TestClusterNameFromARN(t *testing.T) {
	cases := map[string]string{
		"arn:aws:eks:us-east-1:123456789012:cluster/prod":    "prod",
		"arn:aws-cn:eks:cn-north-1:123456789012:cluster/dev": "dev",
		"prod.us-east-1.eksctl.io":                           "",
		"arn:aws:iam::123456789012:role/x":                   "",
	}
	for in, want := range cases {
		if got := clusterNameFromARN(in); got != want {
			t.Errorf("clusterNameFromARN(%q) = %q, want %q", in, got, want)
		}
	}
}

func kubeconfigFor(cluster string) string {
	return `apiVersion: v1
kind: Config
current-context: ctx
contexts:
- name: ctx
  context:
    cluster: "` + cluster + `"
    user: u
clusters:
- name: "` + cluster + `"
  cluster:
    server: https://ABCDEF0123456789.gr7.us-east-1.eks.amazonaws.com
users:
- name: u
  user: {}
`
}
