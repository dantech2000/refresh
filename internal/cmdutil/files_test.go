package cmdutil_test

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/dantech2000/refresh/internal/cmdutil"
)

// tree returns the content of each regular file in dir, and the target of
// each symbolic link as "-> target".
func tree(t *testing.T, dir string) map[string]string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, e := range entries {
		path := filepath.Join(dir, e.Name())
		if e.Type()&os.ModeSymlink != 0 {
			target, err := os.Readlink(path)
			if err != nil {
				t.Fatal(err)
			}
			got[e.Name()] = "-> " + target
			continue
		}
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		got[e.Name()] = string(b)
	}
	return got
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestGenerateFiles(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "page.1"), "old page")
	write(t, filepath.Join(dir, "other.1"), "another tool")

	err := cmdutil.GenerateFiles(dir, func(tmp string) error {
		if filepath.Dir(tmp) != dir {
			t.Errorf("temporary directory %s is not inside %s", tmp, dir)
		}
		write(t, filepath.Join(tmp, "page.1"), "new page")
		write(t, filepath.Join(tmp, "new.1"), "new command")
		return nil
	})
	if err != nil {
		t.Fatalf("GenerateFiles() error = %v", err)
	}

	want := map[string]string{"page.1": "new page", "new.1": "new command", "other.1": "another tool"}
	if diff := cmp.Diff(want, tree(t, dir)); diff != "" {
		t.Errorf("directory (-want +got):\n%s", diff)
	}
}

// A symbolic link at the path of a generated file is replaced. The file
// that it points to keeps its content.
func TestGenerateFilesReplacesSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("making a symbolic link needs a privilege on Windows")
	}
	dir := t.TempDir()
	target := filepath.Join(t.TempDir(), "victim")
	write(t, target, "do not touch")
	if err := os.Symlink(target, filepath.Join(dir, "page.1")); err != nil {
		t.Fatal(err)
	}

	err := cmdutil.GenerateFiles(dir, func(tmp string) error {
		write(t, filepath.Join(tmp, "page.1"), "new page")
		return nil
	})
	if err != nil {
		t.Fatalf("GenerateFiles() error = %v", err)
	}

	if diff := cmp.Diff(map[string]string{"page.1": "new page"}, tree(t, dir)); diff != "" {
		t.Errorf("directory (-want +got):\n%s", diff)
	}
	if b, err := os.ReadFile(target); err != nil || string(b) != "do not touch" {
		t.Errorf("link target = %q, %v; want it unchanged", b, err)
	}
}

func TestGenerateFilesFailure(t *testing.T) {
	failed := errors.New("generator failed")
	tests := []struct {
		name     string
		generate func(tmp string) error
		wantErr  error
	}{
		{
			name: "generator fails after a file",
			generate: func(tmp string) error {
				if err := os.WriteFile(filepath.Join(tmp, "page.1"), []byte("half"), 0o600); err != nil {
					return err
				}
				return failed
			},
			wantErr: failed,
		},
		{
			name: "generator makes a directory",
			generate: func(tmp string) error {
				return os.Mkdir(filepath.Join(tmp, "sub"), 0o700)
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			write(t, filepath.Join(dir, "page.1"), "old page")

			err := cmdutil.GenerateFiles(dir, tt.generate)

			if err == nil || (tt.wantErr != nil && !errors.Is(err, tt.wantErr)) {
				t.Errorf("GenerateFiles() error = %v, want %v", err, tt.wantErr)
			}
			// The old file is whole, and the temporary directory is gone.
			if diff := cmp.Diff(map[string]string{"page.1": "old page"}, tree(t, dir)); diff != "" {
				t.Errorf("directory after a failure (-want +got):\n%s", diff)
			}
		})
	}
}

func TestGenerateFilesMissingDirectory(t *testing.T) {
	err := cmdutil.GenerateFiles(filepath.Join(t.TempDir(), "missing"), func(string) error {
		t.Error("the generator ran without a destination")
		return nil
	})
	if err == nil {
		t.Error("GenerateFiles() error = nil for a directory that does not exist")
	}
}

// A page that cannot be installed stops the run before the first rename, so
// the pages that sort before it are not replaced.
func TestGenerateFilesDirectoryAtDestination(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.md"), []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "b.md"), 0o755); err != nil {
		t.Fatal(err)
	}

	err := cmdutil.GenerateFiles(dir, func(tmp string) error {
		for _, name := range []string{"a.md", "b.md"} {
			if err := os.WriteFile(filepath.Join(tmp, name), []byte("new"), 0o644); err != nil {
				return err
			}
		}
		return nil
	})
	if err == nil {
		t.Fatal("GenerateFiles() = nil, want an error for the directory at b.md")
	}
	got, err := os.ReadFile(filepath.Join(dir, "a.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "old" {
		t.Errorf("a.md = %q, want %q: nothing is installed when one page cannot be", got, "old")
	}
}
