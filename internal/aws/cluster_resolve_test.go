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

// NonInteractive never prompts, even on a TTY: a mutating command then fails
// on a partial match and names the candidate; an exact name still resolves.
func TestResolveClusterName_NonInteractiveOnTTYNeverPrompts(t *testing.T) {
	withTTY(t, true)
	origPrompt := promptLine
	promptLine = func(context.Context) (string, error) {
		t.Fatal("NonInteractive resolution prompted")
		return "", nil
	}
	t.Cleanup(func() { promptLine = origPrompt })
	opts := ClusterNameOptions{NonInteractive: true}

	_, err := resolveClusterName(context.Background(), clustersAPI("prod-legacy", "staging"), "prod", opts)
	if err == nil || !strings.Contains(err.Error(), "prod-legacy") {
		t.Fatalf("err = %v, want an error naming the candidate prod-legacy", err)
	}
	_, err = resolveClusterName(context.Background(), clustersAPI("prod-east", "prod-west"), "prod", opts)
	if err == nil || !strings.Contains(err.Error(), "prod-east") {
		t.Fatalf("err = %v, want an error listing the candidates", err)
	}
	got, err := resolveClusterName(context.Background(), clustersAPI("prod-legacy", "prod"), "prod", opts)
	if err != nil || got != "prod" {
		t.Fatalf("exact name: got %q, %v; want prod", got, err)
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

// A ListClusters permission failure carries the IAM help once. ListAllPages
// formats the error; resolveClusterName must not format it again.
func TestResolveClusterName_ListFailureIAMHelpOnce(t *testing.T) {
	m := mocks.NewEKSAPI().Build()
	m.ListClustersFn = func(context.Context, *eks.ListClustersInput, ...func(*eks.Options)) (*eks.ListClustersOutput, error) {
		return nil, mocks.AccessDenied()
	}
	_, err := resolveClusterName(context.Background(), m, "prod", ClusterNameOptions{ReadOnly: true})
	if err == nil {
		t.Fatal("expected an error")
	}
	if n := strings.Count(err.Error(), "Required permissions"); n != 1 {
		t.Errorf("IAM help appears %d times, want 1:\n%s", n, err)
	}
}

// An unknown REFRESH_CONTEXT fails cluster resolution instead of falling
// back to the kubeconfig or the saved current context.
func TestResolveClusterPattern_UnknownRefreshContextErrors(t *testing.T) {
	dir := isolateConfig(t)
	writeFile(t, filepath.Join(dir, "context.yaml"), "current: prod\ncontexts:\n  prod:\n    cluster: prod-eks\n")
	t.Setenv("REFRESH_CONTEXT", "prdo")
	if _, _, err := resolveClusterPattern("", true); err == nil || !strings.Contains(err.Error(), "prdo") {
		t.Fatalf("err = %v, want an unknown-context error", err)
	}
}

func TestResolveClusterPattern_FlagWins(t *testing.T) {
	dir := isolateConfig(t)
	writeFile(t, filepath.Join(dir, "context.yaml"), "current: p\ncontexts:\n  p:\n    cluster: from-context\n")
	for _, readOnly := range []bool{true, false} {
		got, fromCtx, err := resolveClusterPattern("from-flag", readOnly)
		if err != nil || got != "from-flag" || fromCtx != "" {
			t.Errorf("readOnly=%v: got %q (ctx %q), %v; want from-flag", readOnly, got, fromCtx, err)
		}
	}
}

// The active refresh context is an explicit user choice, so both read-only
// and mutating commands use it, and report which context it came from.
func TestResolveClusterPattern_ActiveContextBeforeKubeconfig(t *testing.T) {
	dir := isolateConfig(t)
	writeFile(t, filepath.Join(dir, "context.yaml"), "current: p\ncontexts:\n  p:\n    cluster: from-context\n")
	kc := filepath.Join(dir, "kubeconfig")
	writeFile(t, kc, kubeconfigFor("from-kubeconfig"))
	t.Setenv("KUBECONFIG", kc)

	for _, readOnly := range []bool{true, false} {
		got, fromCtx, err := resolveClusterPattern("", readOnly)
		if err != nil || got != "from-context" || fromCtx != "p" {
			t.Errorf("readOnly=%v: got %q (ctx %q), %v; want from-context from p", readOnly, got, fromCtx, err)
		}
	}
}

func TestResolveClusterPattern_ReadOnlyKubeconfigFallback(t *testing.T) {
	dir := isolateConfig(t)
	kc := filepath.Join(dir, "kubeconfig")
	writeFile(t, kc, kubeconfigFor("arn:aws:eks:us-east-1:123456789012:cluster/from-kubeconfig"))
	t.Setenv("KUBECONFIG", kc)

	got, _, err := resolveClusterPattern("", true)
	if err != nil || got != "from-kubeconfig" {
		t.Errorf("got %q, %v; want from-kubeconfig", got, err)
	}
}

// Safety: a mutating command must never take its target from the kubeconfig
// current context. `cluster upgrade -c "$CLUSTER" --yes` with an empty
// $CLUSTER on a runner whose kubeconfig points at prod must fail.
func TestResolveClusterPattern_MutatingIgnoresKubeconfig(t *testing.T) {
	dir := isolateConfig(t)
	kc := filepath.Join(dir, "kubeconfig")
	writeFile(t, kc, kubeconfigFor("prod"))
	t.Setenv("KUBECONFIG", kc)

	got, _, err := resolveClusterPattern("", false)
	if !errors.Is(err, ErrNoClusterSpecified) {
		t.Fatalf("got %q, %v; want ErrNoClusterSpecified", got, err)
	}
}

func TestResolveClusterPattern_NothingResolvesWrapsSentinel(t *testing.T) {
	isolateConfig(t)
	for _, readOnly := range []bool{true, false} {
		_, _, err := resolveClusterPattern("", readOnly)
		if !errors.Is(err, ErrNoClusterSpecified) {
			t.Errorf("readOnly=%v: want ErrNoClusterSpecified, got %v", readOnly, err)
		}
	}
}

// A kubeconfig that fails to load keeps both the sentinel and the load error
// in the chain (%w, not %v).
func TestResolveClusterPattern_BrokenKubeconfigWrapsCause(t *testing.T) {
	dir := isolateConfig(t)
	kc := filepath.Join(dir, "kubeconfig")
	writeFile(t, kc, "{not: [valid")
	t.Setenv("KUBECONFIG", kc)

	_, _, err := resolveClusterPattern("", true)
	if !errors.Is(err, ErrNoClusterSpecified) {
		t.Fatalf("err = %v, want ErrNoClusterSpecified", err)
	}
	multi, ok := err.(interface{ Unwrap() []error })
	if !ok || len(multi.Unwrap()) != 2 {
		t.Fatalf("err = %v, want it to wrap the sentinel and the kubeconfig error", err)
	}
	if !strings.Contains(err.Error(), "loading kubeconfig") {
		t.Errorf("err = %v, want the kubeconfig load cause", err)
	}
}

// fakeSpinner records whether it is still running.
type fakeSpinner struct{ running bool }

func (s *fakeSpinner) Start() error   { s.running = true; return nil }
func (s *fakeSpinner) Success(string) { s.running = false }
func (s *fakeSpinner) Stop()          { s.running = false }

// Both prompt paths (single partial match, multiple matches) must run after
// the spinner stops; a running spinner redraws its line and erases the prompt.
func TestResolveClusterName_SpinnerStoppedBeforePrompt(t *testing.T) {
	cases := []struct {
		name     string
		clusters []string
		answer   string
		want     string
	}{
		{name: "single partial match", clusters: []string{"prod-legacy"}, answer: "y", want: "prod-legacy"},
		{name: "multiple matches", clusters: []string{"prod-east", "prod-west"}, answer: "2", want: "prod-west"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withTTY(t, true)
			spin := &fakeSpinner{}
			origSpinner, origPrompt := newResolveSpinner, promptLine
			t.Cleanup(func() { newResolveSpinner, promptLine = origSpinner, origPrompt })
			newResolveSpinner = func() resolveSpinner { return spin }
			prompted := false
			promptLine = func(context.Context) (string, error) {
				prompted = true
				if spin.running {
					t.Error("prompt shown while the spinner is still running")
				}
				return tc.answer, nil
			}

			got, err := resolveClusterName(context.Background(), clustersAPI(tc.clusters...), "prod", ClusterNameOptions{})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !prompted {
				t.Fatal("expected a prompt")
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
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
