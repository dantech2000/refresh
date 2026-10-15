package cluster_test

import (
	"strings"
	"testing"

	"github.com/dantech2000/refresh/internal/cmdutil"
	"github.com/dantech2000/refresh/internal/cmdutil/cmdutiltest"
	"github.com/dantech2000/refresh/internal/contexts"
)

// A name that EKS cannot have is refused before any call. A cluster name is
// checked from every source: the argument, -c, REFRESH_CLUSTER, and the
// active context.
// An argument or a flag that is given empty is refused too; it does not
// fall back to the defaults.
func TestNamesAreCheckedLocally(t *testing.T) {
	const rule = "a name has 1 to 100 characters, only letters, digits, hyphens, and underscores, and starts with a letter or digit"
	tests := []struct {
		name    string
		args    []string
		env     map[string]string
		context string // the cluster of the active context, when not ""
		wantErr string
	}{
		{name: "a space", args: []string{"cluster", "describe", "my cluster"}, wantErr: `invalid cluster name "my cluster": ` + rule},
		{name: "Unicode", args: []string{"cluster", "describe", "prød"}, wantErr: `invalid cluster name "prød": ` + rule},
		{
			name:    "300 characters",
			args:    []string{"cluster", "describe", strings.Repeat("x", 300)},
			wantErr: `invalid cluster name "` + strings.Repeat("x", 40) + `"... (300 characters): ` + rule,
		},
		{name: "the -c flag", args: []string{"nodegroup", "list", "-c", "bad name!"}, wantErr: `invalid cluster name "bad name!": ` + rule},
		{
			name:    "a change",
			args:    []string{"cluster", "upgrade", "bad/name", "--to", "1.34", "--dry-run"},
			wantErr: `invalid cluster name "bad/name": ` + rule,
		},
		{
			name:    "REFRESH_CLUSTER",
			args:    []string{"cluster", "describe"},
			env:     map[string]string{cmdutil.EnvCluster: "bad name"},
			wantErr: `invalid cluster name "bad name" (from REFRESH_CLUSTER): ` + rule,
		},
		{
			name:    "the active context",
			args:    []string{"cluster", "describe"},
			context: "bad name",
			wantErr: `invalid cluster name "bad name" (from the active context "ctx"): ` + rule,
		},
		{
			name:    "a nodegroup",
			args:    []string{"nodegroup", "describe", "web 1", "-c", "prod"},
			wantErr: `invalid nodegroup name "web 1": a name has 1 to 63 characters, only letters, digits, hyphens, and underscores, and starts with a letter or digit`,
		},
		{
			name:    "a nodegroup to scale",
			args:    []string{"nodegroup", "scale", strings.Repeat("n", 64), "-c", "prod", "--desired", "1", "--dry-run"},
			wantErr: `invalid nodegroup name "` + strings.Repeat("n", 40) + `"... (64 characters): a name has 1 to 63 characters, only letters, digits, hyphens, and underscores, and starts with a letter or digit`,
		},
		{
			name:    "a nodegroup to roll",
			args:    []string{"nodegroup", "update", "web", "wéb", "-c", "prod", "--dry-run"},
			wantErr: `invalid nodegroup name "wéb": a name has 1 to 63 characters, only letters, digits, hyphens, and underscores, and starts with a letter or digit`,
		},
		{
			name:    "a nodegroup of an upgrade",
			args:    []string{"cluster", "upgrade", "prod", "--to", "1.34", "-n", "web!", "--dry-run"},
			wantErr: `invalid nodegroup name "web!": a name has 1 to 63 characters, only letters, digits, hyphens, and underscores, and starts with a letter or digit`,
		},
		{
			name:    "an add-on",
			args:    []string{"addon", "describe", "core dns", "-c", "prod"},
			wantErr: `invalid add-on name "core dns": ` + rule,
		},
		{
			name:    "an add-on to skip",
			args:    []string{"addon", "update", "--all", "--skip-addon", "a/b", "-c", "prod", "--dry-run"},
			wantErr: `invalid add-on name "a/b": ` + rule,
		},
		{
			name:    "an empty nodegroup",
			args:    []string{"nodegroup", "describe", "", "-c", "prod"},
			wantErr: "the argument is empty: pass a name",
		},
		{
			name:    "an empty argument",
			args:    []string{"cluster", "describe", ""},
			wantErr: "the argument is empty: pass a name",
		},
		{
			name:    "an empty -c",
			args:    []string{"nodegroup", "list", "-c", ""},
			wantErr: "-c/--cluster is empty: pass a cluster name",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newDescribeFake(t)
			for k, v := range tt.env {
				t.Setenv(k, v)
			}
			if tt.context != "" {
				cmdutiltest.NewContextStore(t, e.Factory, &contexts.File{
					Current:  "ctx",
					Contexts: map[string]contexts.Context{"ctx": {Cluster: tt.context, Region: east}},
				})
			}
			err := e.Run(cmdutiltest.Context(t), tt.args...)
			if err == nil || err.Error() != tt.wantErr {
				t.Fatalf("error = %v\nwant %s", err, tt.wantErr)
			}
			if got := cmdutil.ExitCode(err); got != cmdutil.ExitFailure {
				t.Errorf("exit code = %d, want %d", got, cmdutil.ExitFailure)
			}
			if ops := e.Operations(); len(ops) != 0 {
				t.Errorf("requests = %v, want none", ops)
			}
		})
	}
}
