package gendocs_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/dantech2000/refresh/internal/cmd/root"
	"github.com/dantech2000/refresh/internal/cmdutil/cmdutiltest"
)

// generate runs "refresh gen-docs --out dir" on a new root and returns the
// content of each file that it wrote.
func generate(t *testing.T, dir string) map[string]string {
	t.Helper()
	f, _, out, errOut := cmdutiltest.NewFactory()
	cmd := root.NewCmdRoot(f)
	cmd.SetArgs([]string{"gen-docs", "--out", dir})
	if err := cmd.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("gen-docs: %v", err)
	}
	if want := "Generated the command reference in " + dir + "\n"; out.String() != want {
		t.Errorf("stdout = %q, want %q", out.String(), want)
	}
	if errOut.Len() != 0 {
		t.Errorf("stderr = %q, want nothing", errOut.String())
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	pages := map[string]string{}
	for _, e := range entries {
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		pages[e.Name()] = string(b)
	}
	return pages
}

func TestGenDocs(t *testing.T) {
	// The directory does not exist yet: the command makes it.
	pages := generate(t, filepath.Join(t.TempDir(), "reference"))

	for _, name := range []string{
		"refresh.md", "refresh_version.md", "refresh_use.md", "refresh_current.md",
		"refresh_context.md", "refresh_context_add.md", "refresh_context_list.md",
		"refresh_context_remove.md", "refresh_install-man.md", "refresh_completion_zsh.md",
	} {
		if _, ok := pages[name]; !ok {
			t.Errorf("no page %s", name)
		}
	}
	for name, content := range pages {
		if strings.Contains(name, "gen-docs") {
			t.Errorf("page %s documents a hidden command", name)
		}
		if strings.Contains(content, "Auto generated") {
			t.Errorf("page %s has cobra's footer, which holds the date of the run", name)
		}
	}
	add := pages["refresh_context_add.md"]
	for _, want := range []string{
		"## refresh context add", "Add or update a saved context", "### Examples",
		"-c, --cluster string", "### Options inherited from parent commands", "--max-concurrency int",
		"[refresh context](refresh_context.md)",
	} {
		if !strings.Contains(add, want) {
			t.Errorf("refresh_context_add.md does not contain %q", want)
		}
	}
	if t.Failed() {
		t.Logf("refresh_context_add.md:\n%s", add)
	}
}

func TestGenDocsIsDeterministic(t *testing.T) {
	first := generate(t, t.TempDir())
	second := generate(t, t.TempDir())
	if diff := cmp.Diff(first, second); diff != "" {
		t.Errorf("two runs differ (-first +second):\n%s", diff)
	}
}

func TestGenDocsErrors(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{name: "no output directory", args: []string{"gen-docs"}, wantErr: `required flag(s) "out" not set`},
		{name: "argument", args: []string{"gen-docs", "docs"}, wantErr: `unknown command "docs" for "refresh gen-docs"`},
		{
			name:    "a file is in the way",
			args:    []string{"gen-docs", "--out", filepath.Join(blocker, "reference")},
			wantErr: "creating " + filepath.Join(blocker, "reference"),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f, _, out, _ := cmdutiltest.NewFactory()
			cmd := root.NewCmdRoot(f)
			cmd.SetArgs(tt.args)
			err := cmd.ExecuteContext(context.Background())
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error = %v, want one that contains %q", err, tt.wantErr)
			}
			if out.Len() != 0 {
				t.Errorf("stdout = %q, want nothing", out.String())
			}
		})
	}
}
