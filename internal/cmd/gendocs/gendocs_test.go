package gendocs_test

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/dantech2000/refresh/internal/cmd/root"
	"github.com/dantech2000/refresh/internal/cmdutil/cmdutiltest"
)

// generate runs "refresh gen-docs --out dir" on a new root, with more
// arguments when given, and returns the content of each file in dir.
func generate(t *testing.T, dir string, args ...string) map[string]string {
	t.Helper()
	f, _, out, errOut := cmdutiltest.NewFactory()
	cmd := root.NewCmdRoot(f)
	cmd.SetArgs(append([]string{"gen-docs", "--out", dir}, args...))
	if err := cmd.ExecuteContext(context.Background()); err != nil {
		t.Fatalf("gen-docs: %v", err)
	}
	what := "the command reference"
	if len(args) > 0 {
		what = "the man pages"
	}
	if want := "Generated " + what + " in " + dir + "\n"; out.String() != want {
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
		"refresh.md", "refresh_version.md", "refresh_context_use.md", "refresh_context_current.md",
		"refresh_context.md", "refresh_context_add.md", "refresh_context_list.md",
		"refresh_context_remove.md", "refresh_completion_zsh.md",
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
		{
			name:    "unknown format",
			args:    []string{"gen-docs", "--out", t.TempDir(), "--format", "html"},
			wantErr: `invalid --format "html": must be one of: markdown, man`,
		},
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

// A symbolic link at the path of a page is replaced by the page. The file
// that it points to keeps its content, and other files stay.
func TestGenDocsDoesNotWriteThroughSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("making a symbolic link needs a privilege on Windows")
	}
	dir := t.TempDir()
	victim := filepath.Join(t.TempDir(), "victim")
	for path, content := range map[string]string{victim: "do not touch", filepath.Join(dir, "schemas.md"): "hand-written"} {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(victim, filepath.Join(dir, "refresh.md")); err != nil {
		t.Fatal(err)
	}

	pages := generate(t, dir)

	if !strings.HasPrefix(pages["refresh.md"], "## refresh\n") {
		t.Errorf("refresh.md is not the generated page:\n%.200s", pages["refresh.md"])
	}
	if info, err := os.Lstat(filepath.Join(dir, "refresh.md")); err != nil || !info.Mode().IsRegular() {
		t.Errorf("refresh.md: mode %v, error %v; want a regular file in place of the link", info.Mode(), err)
	}
	if b, err := os.ReadFile(victim); err != nil || string(b) != "do not touch" {
		t.Errorf("link target = %q, %v; want it unchanged", b, err)
	}
	if pages["schemas.md"] != "hand-written" {
		t.Errorf("schemas.md = %q, want it unchanged", pages["schemas.md"])
	}
	for name := range pages {
		if strings.HasPrefix(name, ".refresh-tmp-") {
			t.Errorf("temporary directory %s is still there", name)
		}
	}
}

func TestGenDocsMan(t *testing.T) {
	// A fixed date makes two runs write the same bytes.
	t.Setenv("SOURCE_DATE_EPOCH", "1768435200") // the middle of January 2026 in every time zone
	dir := filepath.Join(t.TempDir(), "man1")

	pages := generate(t, dir, "--format", "man")

	for _, name := range []string{
		"refresh.1", "refresh-version.1", "refresh-context.1", "refresh-context-use.1",
		"refresh-context-current.1", "refresh-completion-zsh.1",
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
			t.Errorf("page %s has cobra's footer", name)
		}
	}
	use := pages["refresh-context-use.1"]
	// The build of the test factory is v1.2.3.
	for _, want := range []string{`.TH "REFRESH" "1" "Jan 2026" "refresh v1.2.3" "refresh manual"`, ".SH NAME", "refresh-context-use - Switch the active context", ".SH EXAMPLE"} {
		if !strings.Contains(use, want) {
			t.Errorf("refresh-context-use.1 does not contain %q", want)
		}
	}
	if t.Failed() {
		t.Logf("refresh-context-use.1:\n%s", use)
	}
	if diff := cmp.Diff(pages, generate(t, t.TempDir(), "--format", "man")); diff != "" {
		t.Errorf("two runs with one SOURCE_DATE_EPOCH differ (-first +second):\n%s", diff)
	}
}
