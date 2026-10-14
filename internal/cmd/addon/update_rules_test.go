package addon_test

import (
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/dantech2000/refresh/internal/cmd/cmdtest"
	"github.com/dantech2000/refresh/internal/cmd/root"
	"github.com/dantech2000/refresh/internal/cmdutil/cmdutiltest"
	"github.com/dantech2000/refresh/internal/kube/kubetest"
)

// kubeconfigOfProd points KUBECONFIG at a file whose current context is
// the cluster prod of the fake.
func kubeconfigOfProd(t *testing.T, e *cmdtest.Fake) {
	t.Helper()
	srv := kubetest.NewServer(e.World, east, "prod")
	t.Cleanup(srv.Close)
	path := filepath.Join(t.TempDir(), "kubeconfig")
	if err := srv.WriteKubeconfig(path); err != nil {
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
