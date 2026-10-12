package installman

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/spf13/cobra"

	"github.com/dantech2000/refresh/internal/cmdutil/cmdutiltest"
)

func TestManDir(t *testing.T) {
	tests := []struct {
		name string
		// blocked lists the candidate directories that cannot be written,
		// relative to the home directory.
		blocked []string
		want    string
	}{
		{name: "standard directory", want: ".local/share/man/man1"},
		{name: "first is blocked", blocked: []string{".local/share"}, want: ".local/man/man1"},
		{name: "first two are blocked", blocked: []string{".local"}, want: "man/man1"},
		{
			// Nothing can be written: the standard directory is the answer,
			// and the caller reports that it cannot write there.
			name: "all are blocked", blocked: []string{".local", "man"}, want: ".local/share/man/man1",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home := t.TempDir()
			for _, b := range tt.blocked {
				// A file where a directory must be blocks every path below it.
				path := filepath.Join(home, filepath.FromSlash(b))
				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, nil, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			want := filepath.Join(home, filepath.FromSlash(tt.want))
			if got := manDir(home); got != want {
				t.Errorf("manDir() = %q, want %q", got, want)
			}
		})
	}
}

func TestIsWritableDir(t *testing.T) {
	base := t.TempDir()
	file := filepath.Join(base, "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		dir  string
		want bool
	}{
		{name: "existing directory", dir: base, want: true},
		{name: "directory that it can make", dir: filepath.Join(base, "a", "b", "man1"), want: true},
		{name: "below a file", dir: filepath.Join(file, "man1"), want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isWritableDir(tt.dir); got != tt.want {
				t.Errorf("isWritableDir() = %t, want %t", got, tt.want)
			}
		})
	}
	// The test file is removed again.
	entries, err := os.ReadDir(base)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".write_test_") {
			t.Errorf("isWritableDir left %s behind", e.Name())
		}
	}
}

// The index command must name the directory where the pages are. Without
// it, mandb rebuilds only the system databases.
func TestManDBCommand(t *testing.T) {
	const dir = "/home/u/.local/share/man"
	tests := []struct {
		goos     string
		wantName string
		wantArgs []string
	}{
		{goos: "darwin", wantName: "makewhatis", wantArgs: []string{dir}},
		{goos: "linux", wantName: "mandb", wantArgs: []string{"-q", dir}},
		{goos: "windows"},
		{goos: "freebsd"},
	}
	for _, tt := range tests {
		t.Run(tt.goos, func(t *testing.T) {
			name, args := manDBCommand(tt.goos, dir)
			if name != tt.wantName {
				t.Errorf("name = %q, want %q", name, tt.wantName)
			}
			if diff := cmp.Diff(tt.wantArgs, args); diff != "" {
				t.Errorf("args (-want +got):\n%s", diff)
			}
		})
	}
}

// newRoot returns a small command tree to write man pages for.
func newRoot() *cobra.Command {
	root := &cobra.Command{Use: "refresh", Short: "EKS upgrade companion"}
	root.AddCommand(&cobra.Command{
		Use:   "version",
		Short: "Print the version of refresh",
		Run:   func(*cobra.Command, []string) {},
	})
	return root
}

func TestRun(t *testing.T) {
	tests := []struct {
		name string
		goos string
		// manpath is what the manpath program prints; {home} stands for
		// the home directory of the test.
		manpath    string
		manpathErr error
		wantInPath bool
		wantCalls  []string // {home} as above
	}{
		{
			name: "directory is in the man path", goos: "darwin",
			manpath:    "/usr/share/man: {home}/.local/share/man :/opt/man\n",
			wantInPath: true,
			wantCalls:  []string{"manpath", "makewhatis {home}/.local/share/man"},
		},
		{
			name: "directory is not in the man path", goos: "linux",
			manpath:   "/usr/share/man:/opt/man\n",
			wantCalls: []string{"manpath", "mandb -q {home}/.local/share/man"},
		},
		{
			name: "no manpath program", goos: "linux",
			manpathErr: errors.New("executable file not found"),
			wantCalls:  []string{"manpath", "mandb -q {home}/.local/share/man"},
		},
		{
			name: "no index tool on this system", goos: "windows",
			manpath:   "/usr/share/man\n",
			wantCalls: []string{"manpath"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home := t.TempDir()
			expand := func(s string) string { return strings.ReplaceAll(s, "{home}", home) }
			ios, _, out, errOut := cmdutiltest.NewIOStreams()
			var calls []string
			o := &options{
				io:      ios,
				version: "v1.2.3",
				home:    home,
				goos:    tt.goos,
				output: func(_ context.Context, name string, args ...string) ([]byte, error) {
					calls = append(calls, strings.Join(append([]string{name}, args...), " "))
					if name == "manpath" {
						return []byte(expand(tt.manpath)), tt.manpathErr
					}
					// The index tool is optional: its failure changes nothing.
					return nil, errors.New("index failed")
				},
			}

			if err := o.run(context.Background(), newRoot()); err != nil {
				t.Fatalf("run() error = %v", err)
			}

			base := filepath.Join(home, ".local", "share", "man")
			for _, page := range []string{"refresh.1", "refresh-version.1"} {
				b, err := os.ReadFile(filepath.Join(base, "man1", page))
				if err != nil {
					t.Errorf("page %s: %v", page, err)
					continue
				}
				for _, want := range []string{`.TH "REFRESH" "1"`, "refresh v1.2.3", ".SH NAME"} {
					if !strings.Contains(string(b), want) {
						t.Errorf("page %s does not contain %q", page, want)
					}
				}
			}
			wantOut := "Man pages installed in: " + filepath.Join(base, "man1") + "\n"
			if tt.wantInPath {
				wantOut += "You can now use 'man refresh' to view the documentation.\n"
			} else {
				wantOut += "\nTo make the man pages accessible, add the following to your shell profile:\n" +
					"  export MANPATH=\"" + base + ":$MANPATH\"\n" +
					"\nOr run temporarily:\n" +
					"  MANPATH=\"" + base + ":$MANPATH\" man refresh\n"
			}
			if diff := cmp.Diff(wantOut, out.String()); diff != "" {
				t.Errorf("stdout (-want +got):\n%s", diff)
			}
			if errOut.Len() != 0 {
				t.Errorf("stderr = %q, want nothing", errOut.String())
			}
			var wantCalls []string
			for _, c := range tt.wantCalls {
				wantCalls = append(wantCalls, expand(c))
			}
			if diff := cmp.Diff(wantCalls, calls); diff != "" {
				t.Errorf("programs run (-want +got):\n%s", diff)
			}
		})
	}
}

func TestRunCannotWrite(t *testing.T) {
	// The home directory is a file, so no man directory can be made.
	home := filepath.Join(t.TempDir(), "home")
	if err := os.WriteFile(home, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	ios, _, out, _ := cmdutiltest.NewIOStreams()
	o := &options{
		io: ios, home: home, goos: "linux",
		output: func(context.Context, string, ...string) ([]byte, error) {
			t.Error("a program ran after the install failed")
			return nil, nil
		},
	}

	err := o.run(context.Background(), newRoot())

	want := "cannot write to directory " + filepath.Join(home, ".local", "share", "man", "man1") + ": permission denied"
	if err == nil || err.Error() != want {
		t.Errorf("run() error = %v, want %q", err, want)
	}
	if out.Len() != 0 {
		t.Errorf("stdout = %q, want nothing", out.String())
	}
}

// TestOutputHonorsContext runs a real program with a cancelled context.
func TestOutputHonorsContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := output(ctx, os.Args[0], "-test.run=^$"); err == nil {
		t.Error("output() error = nil with a cancelled context")
	}
}

func TestOutput(t *testing.T) {
	got, err := output(context.Background(), os.Args[0], "-test.run=^$")
	if err != nil {
		t.Fatalf("output() error = %v", err)
	}
	if !strings.Contains(string(got), "PASS") && !strings.Contains(string(got), "no tests to run") {
		t.Errorf("output() = %q, want the output of the test binary", got)
	}
}
