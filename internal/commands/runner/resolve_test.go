package runner

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"

	awsinternal "github.com/dantech2000/refresh/internal/aws"
)

// noClusterEnv removes every cluster source (flag aside): no active refresh
// context and no kubeconfig.
func noClusterEnv(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("REFRESH_CONFIG_HOME", dir)
	t.Setenv("REFRESH_CONTEXT", "")
	t.Setenv(ClusterEnvVar, "")
	t.Setenv("KUBECONFIG", filepath.Join(dir, "missing-kubeconfig"))
}

// A mutating command with nothing to resolve must fail, not list clusters
// and exit 0 (e.g. `cluster upgrade -c "$CLUSTER"` with an empty variable).
func TestResolveCluster_NoClusterIsAnError(t *testing.T) {
	noClusterEnv(t)
	cmd := newTestCommand(t, nil, map[string]string{"cluster": ""})

	_, err := ResolveCluster(context.Background(), aws.Config{}, cmd)
	if !errors.Is(err, awsinternal.ErrNoClusterSpecified) {
		t.Fatalf("want ErrNoClusterSpecified, got %v", err)
	}
}

// A mutating command never falls back to the kubeconfig current cluster:
// `cluster upgrade -c "$CLUSTER" --yes` with an empty $CLUSTER on a runner
// whose kubeconfig points at prod must fail rather than upgrade prod.
func TestResolveCluster_IgnoresKubeconfigCurrentCluster(t *testing.T) {
	noClusterEnv(t)
	kc := filepath.Join(t.TempDir(), "kubeconfig")
	body := "apiVersion: v1\nkind: Config\ncurrent-context: c\ncontexts:\n- name: c\n  context: {cluster: prod, user: u}\nclusters:\n- name: prod\n  cluster: {server: https://example.invalid}\nusers:\n- name: u\n  user: {}\n"
	if err := os.WriteFile(kc, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KUBECONFIG", kc)
	cmd := newTestCommand(t, nil, map[string]string{"cluster": ""})

	_, err := ResolveCluster(context.Background(), aws.Config{}, cmd)
	if !errors.Is(err, awsinternal.ErrNoClusterSpecified) {
		t.Fatalf("want ErrNoClusterSpecified, got %v", err)
	}
}

// A read-only command with nothing to resolve returns an error (non-zero
// exit), and in a machine format prints nothing to stdout.
func TestResolveClusterOrList_NoClusterMachineFormat(t *testing.T) {
	noClusterEnv(t)
	for _, format := range []string{"json", "yaml"} {
		t.Run(format, func(t *testing.T) {
			cmd := newTestCommand(t, nil, map[string]string{"cluster": "", "format": format})

			var (
				listed bool
				err    error
			)
			out := captureStdout(t, func() {
				_, listed, err = ResolveClusterOrList(context.Background(), aws.Config{}, cmd)
			})
			if !listed {
				t.Error("listed = false, want true")
			}
			if !errors.Is(err, awsinternal.ErrNoClusterSpecified) {
				t.Errorf("want ErrNoClusterSpecified, got %v", err)
			}
			if out != "" {
				t.Errorf("stdout must stay empty in -o %s, got %q", format, out)
			}
		})
	}
}
