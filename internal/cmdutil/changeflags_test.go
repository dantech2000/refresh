package cmdutil_test

import (
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/dantech2000/refresh/internal/cmdutil"
)

func TestChangeFlagsValidate(t *testing.T) {
	valid := cmdutil.ChangeFlags{PollInterval: 15 * time.Second, WaitTimeout: 5 * time.Minute}
	tests := []struct {
		name string
		edit func(*cmdutil.ChangeFlags)
		want string // a part of the error, or "" for none
	}{
		{name: "the defaults", edit: func(*cmdutil.ChangeFlags) {}},
		{name: "no wait limit", edit: func(f *cmdutil.ChangeFlags) { f.WaitTimeout = 0 }},
		{name: "dry run with no wait", edit: func(f *cmdutil.ChangeFlags) { f.DryRun, f.NoWait = true, true }, want: "--dry-run changes nothing"},
		{name: "poll interval zero", edit: func(f *cmdutil.ChangeFlags) { f.PollInterval = 0 }, want: "invalid --poll-interval 0s"},
		{name: "poll interval negative", edit: func(f *cmdutil.ChangeFlags) { f.PollInterval = -time.Second }, want: "invalid --poll-interval -1s"},
		{name: "wait timeout negative", edit: func(f *cmdutil.ChangeFlags) { f.WaitTimeout = -time.Second }, want: "invalid --wait-timeout -1s"},
		{
			name: "skip and require", edit: func(f *cmdutil.ChangeFlags) { f.SkipHealthCheck, f.RequireHealthy = true, true },
			want: "--skip-health-check and --require-healthy cannot be used together",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := valid
			tt.edit(&f)
			err := f.Validate()
			switch {
			case tt.want == "" && err != nil:
				t.Errorf("error = %v, want none", err)
			case tt.want != "" && (err == nil || !strings.Contains(err.Error(), tt.want)):
				t.Errorf("error = %v, want %q", err, tt.want)
			}
		})
	}
}

// A command with later steps has no --no-wait, and a command that drains
// nothing has no --force.
func TestAddChangeFlags(t *testing.T) {
	shared := []string{"dry-run", "yes", "quiet", "wait-timeout", "poll-interval", "skip-health-check", "require-healthy"}
	tests := []struct {
		name          string
		noWait, force bool
	}{
		{name: "addon update", noWait: true},
		{name: "nodegroup update", noWait: true, force: true},
		{name: "cluster upgrade", force: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var f cmdutil.ChangeFlags
			cmd := &cobra.Command{Use: "x"}
			cmdutil.AddChangeFlags(cmd, &f, 5*time.Minute, "the change", tt.noWait, tt.force)
			for _, name := range shared {
				if cmd.Flags().Lookup(name) == nil {
					t.Errorf("no --%s", name)
				}
			}
			if got := cmd.Flags().Lookup("no-wait") != nil; got != tt.noWait {
				t.Errorf("--no-wait registered = %v, want %v", got, tt.noWait)
			}
			if got := cmd.Flags().Lookup("force") != nil; got != tt.force {
				t.Errorf("--force registered = %v, want %v", got, tt.force)
			}
			if f.WaitTimeout != 5*time.Minute || f.PollInterval != cmdutil.DefaultPollInterval {
				t.Errorf("defaults = %s, %s; want 5m0s, 15s", f.WaitTimeout, f.PollInterval)
			}
		})
	}
}
