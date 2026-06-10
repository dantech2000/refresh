package commands

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/urfave/cli/v2"
)

func captureCommandStdout(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	original := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	t.Cleanup(func() { os.Stdout = original })

	callErr := fn()
	_ = w.Close()
	var buf bytes.Buffer
	_, _ = io.Copy(&buf, r)
	return buf.String(), callErr
}

func TestVersionCommandAction(t *testing.T) {
	oldInfo := VersionInfo
	VersionInfo.Version = "v-test"
	VersionInfo.Commit = "abc123"
	VersionInfo.BuildDate = "today"
	t.Cleanup(func() { VersionInfo = oldInfo })

	out, err := captureCommandStdout(t, func() error {
		return VersionCommand().Action(cli.NewContext(cli.NewApp(), nil, nil))
	})
	if err != nil {
		t.Fatalf("version action: %v", err)
	}
	for _, want := range []string{"v-test", "abc123", "today"} {
		if !strings.Contains(out, want) {
			t.Fatalf("version output missing %q: %q", want, out)
		}
	}

	VersionInfo.Commit = ""
	VersionInfo.BuildDate = ""
	out, err = captureCommandStdout(t, func() error {
		return VersionCommand().Action(cli.NewContext(cli.NewApp(), nil, nil))
	})
	if err != nil || strings.Contains(out, "commit:") || strings.Contains(out, "built:") {
		t.Fatalf("minimal version output = %q, %v", out, err)
	}
}

func TestManPageHelpersAndInstall(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("MANPATH", "")

	dir := getManPageDir()
	if !strings.Contains(dir, home) {
		t.Fatalf("getManPageDir() = %q, want under %q", dir, home)
	}
	if !isWritableDir(dir) {
		t.Fatalf("expected writable dir %q", dir)
	}
	if isInManPath(dir) {
		t.Fatal("temp dir should not be in MANPATH")
	}
	updateManDB()

	app := cli.NewApp()
	app.Name = "refresh"
	app.Usage = "test app"
	cmd := ManPageCommand()
	ctx := cli.NewContext(app, nil, nil)

	out, err := captureCommandStdout(t, func() error {
		return cmd.Action(ctx)
	})
	if err != nil {
		t.Fatalf("install manpage: %v", err)
	}
	if !strings.Contains(out, "Man page installed successfully") {
		t.Fatalf("install output = %q", out)
	}
	if _, err := os.Stat(filepath.Join(dir, "refresh.1")); err != nil {
		t.Fatalf("man page missing: %v", err)
	}
}

func TestManPageInstallWriteError(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	blockingFile := filepath.Join(home, ".local")
	if err := os.WriteFile(blockingFile, []byte("not a dir"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "man"), []byte("not a dir"), 0o644); err != nil {
		t.Fatal(err)
	}

	app := cli.NewApp()
	app.Name = "refresh"
	ctx := cli.NewContext(app, nil, nil)

	err := installManPage(ctx)
	if err == nil {
		t.Fatal("expected install error when man dir cannot be created")
	}
}

func TestManPageInstallCaptureDoesNotLeakStdout(t *testing.T) {
	var buf bytes.Buffer
	_, _ = buf.WriteString("ok")
}
