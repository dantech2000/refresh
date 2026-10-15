package root_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/dantech2000/refresh/internal/cmd/root"
	"github.com/dantech2000/refresh/internal/cmdutil/cmdutiltest"
)

// gen-docs writes one reference page for every command of the tree, and
// the page of a command shows its flags and the global ones. A command
// that is added without help text, or that gen-docs does not reach, fails
// here.
func TestGenDocsCoversEveryCommand(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "reference")
	f, _, _, _ := cmdutiltest.NewFactory()
	cmd := root.NewCmdRoot(f)
	cmd.SetArgs([]string{"gen-docs", "--out", dir})
	if err := cmd.ExecuteContext(context.Background()); err != nil {
		t.Fatal(err)
	}

	walk(newTree(), func(c *cobra.Command) {
		if c.Hidden || c.CommandPath() == "refresh help" {
			return
		}
		name := strings.ReplaceAll(c.CommandPath(), " ", "_") + ".md"
		page, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Errorf("%s: no reference page: %v", c.CommandPath(), err)
			return
		}
		if !strings.Contains(string(page), "## "+c.CommandPath()) {
			t.Errorf("%s does not start with the command path", name)
		}
		if c.Runnable() && c.HasParent() && !fromCobra(c) && !strings.Contains(string(page), "### Examples") {
			t.Errorf("%s has no examples", name)
		}
	})

	// The flags of the read commands, one page each.
	for name, flags := range map[string][]string{
		"refresh_status.md":             {"-A, --all-regions", "-f, --filter stringArray", "--sort key", "--desc", "-o, --output format"},
		"refresh_cluster_list.md":       {"-A, --all-regions", "-f, --filter stringArray", "--sort key", "-w, --watch", "--watch-interval duration"},
		"refresh_cluster_describe.md":   {"-o, --output format", "--kubeconfig string", "--kube-context string"},
		"refresh_nodegroup_list.md":     {"-c, --cluster string", "-f, --filter stringArray", "--sort key", "-w, --watch"},
		"refresh_nodegroup_describe.md": {"-c, --cluster string", "--show-instances"},
		"refresh_addon_list.md":         {"-c, --cluster string", "-w, --watch"},
		"refresh_addon_describe.md":     {"-c, --cluster string", "-r, --region strings", "--max-concurrency int"},
		"refresh_addon_update.md": {
			"-c, --cluster string", "--version string", "--all", "--skip-addon strings", "-d, --dry-run", "-y, --yes", "--no-wait",
			"--wait-timeout duration", "--poll-interval duration", "--skip-health-check", "--require-healthy", "-q, --quiet", "-o, --output format",
		},
		"refresh_nodegroup_scale.md": {
			"-c, --cluster string", "--desired int32", "--min int32", "--max int32", "-d, --dry-run", "-y, --yes", "--no-wait", "--force",
			"--wait-timeout duration", "--poll-interval duration", "--skip-health-check", "--require-healthy", "-q, --quiet", "-o, --output format",
		},
		"refresh_cluster_upgrade.md": {
			"--to string", "--only strings", "-n, --nodegroup strings", "--skip-addon strings", "--skip-nodegroup strings", "--skip-insights-check",
			"--skip-verify", "-d, --dry-run", "-y, --yes", "--force", "--wait-timeout duration", "--poll-interval duration", "--skip-health-check",
			"--require-healthy", "-q, --quiet", "-o, --output format",
		},
		"refresh_cluster_rollback.md": {
			"--skip-nodegroup strings", "--skip-insights-check", "--rollback-timeout duration", "--skip-verify", "-d, --dry-run", "-y, --yes",
			"--force", "--wait-timeout duration", "--poll-interval duration", "--skip-health-check", "--require-healthy", "-q, --quiet", "-o, --output format",
		},
	} {
		page, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Errorf("%v", err)
			continue
		}
		for _, flag := range flags {
			if !strings.Contains(string(page), flag) {
				t.Errorf("%s does not document %s", name, flag)
			}
		}
	}
}
