package addon_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/dantech2000/refresh/internal/cmd/cmdtest"
	"github.com/dantech2000/refresh/internal/cmd/root"
	"github.com/dantech2000/refresh/internal/cmdutil"
	"github.com/dantech2000/refresh/internal/cmdutil/cmdutiltest"
	"github.com/dantech2000/refresh/internal/fleet/fleettest"
	"github.com/dantech2000/refresh/internal/kube/kubetest"
)

// kubeconfigOfProd points KUBECONFIG at a file whose current context is
// the cluster prod of the fake, as an EKS context.
func kubeconfigOfProd(t *testing.T, e *cmdtest.Fake) {
	t.Helper()
	srv := kubetest.NewServer(e.World, east, "prod")
	t.Cleanup(srv.Close)
	// An EKS context, as "aws eks update-kubeconfig" writes it: the cluster
	// entry is the ARN of prod. The fallback of a read command counts no
	// other kind of context.
	const arn = "arn:aws:eks:" + east + ":123456789012:cluster/prod"
	kubeconfig := "apiVersion: v1\nkind: Config\ncurrent-context: " + arn + "\n" +
		"clusters:\n- name: " + arn + "\n  cluster: {server: " + srv.URL + "}\n" +
		"contexts:\n- name: " + arn + "\n  context: {cluster: \"" + arn + "\", user: prod}\n" +
		"users:\n- name: prod\n  user: {token: fake-token}\n"
	path := filepath.Join(t.TempDir(), "kubeconfig")
	if err := os.WriteFile(path, []byte(kubeconfig), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KUBECONFIG", path)
}

// "addon update" never takes its cluster from the current kubeconfig
// context, also when that context is the cluster of the world.
func TestUpdate_NoClusterFromAKubeconfigThatNamesOne(t *testing.T) {
	for _, args := range [][]string{{"vpc-cni", "--yes"}, {"vpc-cni", "--dry-run"}, {"--all", "--yes"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			e := cmdtest.NewFake(t, updateWorld(nil), east)
			kubeconfigOfProd(t, e)
			err := e.Run(cmdutiltest.Context(t), append([]string{"addon", "update"}, args...)...)
			if err == nil || !strings.Contains(err.Error(), "no cluster specified") {
				t.Errorf("error = %v, want the no-cluster error", err)
			}
			if n := len(e.Server.Requests()); n != 0 {
				t.Errorf("%d requests, want none", n)
			}
		})
	}
	t.Run("a read command takes it", func(t *testing.T) {
		e := cmdtest.NewFake(t, updateWorld(nil), east)
		kubeconfigOfProd(t, e)
		if err := e.Run(cmdutiltest.Context(t), "addon", "list", "-o", "json"); err != nil {
			t.Fatalf("addon list: %v", err)
		}
	})
}

// The pinned version in the help of "addon update" has an -eksbuild.N
// suffix. A bare version is never in the catalogue of EKS, so an example
// without the suffix always fails.
func TestUpdateHelp_PinnedVersionHasABuild(t *testing.T) {
	e := cmdtest.NewFake(t, updateWorld(nil), east)
	cmd, _, err := root.NewCmdRoot(e.Factory).Find([]string{"addon", "update"})
	if err != nil {
		t.Fatal(err)
	}
	pinned := regexp.MustCompile(`--version\s+(\S+)`).FindAllStringSubmatch(cmd.Example, -1)
	if len(pinned) == 0 {
		t.Fatalf("no example pins a version:\n%s", cmd.Example)
	}
	build := regexp.MustCompile(`^v\d+\.\d+\.\d+-eksbuild\.\d+$`)
	for _, m := range pinned {
		if !build.MatchString(m[1]) {
			t.Errorf("the example pins %q, which is not a version of the EKS catalogue (vX.Y.Z-eksbuild.N)", m[1])
		}
	}
}

// A pinned downgrade on a terminal shows its warning once, and before the
// question, so the answer covers it.
func TestUpdate_DowngradeWarningComesOnceBeforeTheQuestion(t *testing.T) {
	downgrade := "warning: downgrading vpc-cni from " + cniNew + " to " + cniOld
	for _, tt := range []struct {
		answer  string
		exit    int
		updates []string
	}{
		{"y\n", cmdutil.ExitOK, []string{"vpc-cni"}},
		{"n\n", cmdutil.ExitFailure, nil},
	} {
		t.Run(strings.TrimSpace(tt.answer), func(t *testing.T) {
			tc := updateCase{
				seed:   func(c *fleettest.Cluster) { c.Addon("vpc-cni").Version = cniNew },
				args:   []string{"vpc-cni", "--version", cniOld},
				answer: tt.answer, exit: tt.exit, updates: tt.updates,
			}
			e, _ := runUpdate(t, tc)
			said := e.Err.String()
			if n := strings.Count(said, downgrade); n != 1 {
				t.Errorf("the warning is on stderr %d times, want 1:\n%s", n, said)
			}
			warning, question := strings.Index(said, downgrade), strings.Index(said, "[y/N]")
			if question < 0 || warning < 0 || warning > question {
				t.Errorf("the warning is at %d and the question at %d; want the warning first:\n%s", warning, question, said)
			}
		})
	}
}
