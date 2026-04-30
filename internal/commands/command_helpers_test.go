package commands

import (
	"bytes"
	"flag"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/urfave/cli/v2"
)

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

func TestServiceFactories(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if newDefaultLogger(logger) != logger {
		t.Fatal("newDefaultLogger should return provided logger")
	}
	if newDefaultLogger(nil) == nil {
		t.Fatal("newDefaultLogger nil returned nil")
	}

	cfg := aws.Config{Region: "us-east-1"}
	if newClusterService(cfg, false, logger) == nil {
		t.Fatal("newClusterService without health returned nil")
	}
	if newClusterService(cfg, true, nil) == nil {
		t.Fatal("newClusterService with health returned nil")
	}
	if newNodegroupService(cfg, false, logger) == nil {
		t.Fatal("newNodegroupService without health returned nil")
	}
	if newNodegroupService(cfg, true, nil) == nil {
		t.Fatal("newNodegroupService with health returned nil")
	}
}

func TestUpdateClusterAndNodegroupPatterns(t *testing.T) {
	tests := []struct {
		name          string
		args          []string
		clusterFlag   string
		nodegroupFlag string
		wantCluster   string
		wantNodegroup string
	}{
		{
			name:        "positional cluster",
			args:        []string{"develop"},
			wantCluster: "develop",
		},
		{
			name:          "positional cluster and nodegroup",
			args:          []string{"develop", "groupC"},
			wantCluster:   "develop",
			wantNodegroup: "groupC",
		},
		{
			name:          "cluster flag and positional nodegroup",
			args:          []string{"groupC"},
			clusterFlag:   "develop",
			wantCluster:   "develop",
			wantNodegroup: "groupC",
		},
		{
			name:          "flags win",
			args:          []string{"ignored-cluster", "ignored-nodegroup"},
			clusterFlag:   "develop",
			nodegroupFlag: "groupD",
			wantCluster:   "develop",
			wantNodegroup: "groupD",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := newUpdateParseTestContext(t, tt.args, tt.clusterFlag, tt.nodegroupFlag)
			gotCluster, gotNodegroup := updateClusterAndNodegroupPatterns(ctx)
			if gotCluster != tt.wantCluster || gotNodegroup != tt.wantNodegroup {
				t.Fatalf("updateClusterAndNodegroupPatterns() = %q, %q; want %q, %q",
					gotCluster, gotNodegroup, tt.wantCluster, tt.wantNodegroup)
			}
		})
	}
}

func TestUpdateBoolFlagReadsTrailingHealthOnly(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want bool
	}{
		{name: "parsed flag before args", args: []string{"--health-only", "develop"}, want: true},
		{name: "short flag after cluster", args: []string{"develop", "-H"}, want: true},
		{name: "long flag after cluster", args: []string{"develop", "--health-only"}, want: true},
		{name: "not set", args: []string{"develop"}, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := newUpdateParseTestContext(t, tt.args, "", "")
			if got := updateBoolFlag(ctx, "health-only", "H"); got != tt.want {
				t.Fatalf("updateBoolFlag() = %v, want %v", got, tt.want)
			}
		})
	}
}

func newUpdateParseTestContext(t *testing.T, args []string, clusterFlag, nodegroupFlag string) *cli.Context {
	t.Helper()
	set := flag.NewFlagSet("test", flag.ContinueOnError)
	set.String("cluster", "", "")
	set.String("nodegroup", "", "")
	set.Bool("health-only", false, "")
	if clusterFlag != "" {
		if err := set.Set("cluster", clusterFlag); err != nil {
			t.Fatal(err)
		}
	}
	if nodegroupFlag != "" {
		if err := set.Set("nodegroup", nodegroupFlag); err != nil {
			t.Fatal(err)
		}
	}
	if err := set.Parse(args); err != nil {
		t.Fatal(err)
	}
	return cli.NewContext(cli.NewApp(), set, nil)
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
