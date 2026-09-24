package awsconfig

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/dantech2000/refresh/internal/cliconfig"
	"github.com/urfave/cli/v3"
)

// newParsedCommand runs a throwaway command with the given flags and argv and
// returns the parsed *cli.Command for accessor tests.
func newParsedCommand(t *testing.T, flags []cli.Flag, argv ...string) *cli.Command {
	t.Helper()
	var captured *cli.Command
	cmd := &cli.Command{
		Name:  "test",
		Flags: flags,
		Action: func(_ context.Context, c *cli.Command) error {
			captured = c
			return nil
		},
	}
	if err := cmd.Run(context.Background(), append([]string{"test"}, argv...)); err != nil {
		t.Fatal(err)
	}
	return captured
}

func setupContext(t *testing.T, name string, ctx cliconfig.Context) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("REFRESH_CONFIG_HOME", dir)
	t.Setenv("REFRESH_CONTEXT", "")
	t.Setenv("AWS_PROFILE", "")
	t.Setenv("AWS_DEFAULT_PROFILE", "")
	t.Setenv("AWS_REGION", "")
	t.Setenv("AWS_DEFAULT_REGION", "")

	f := &cliconfig.File{Contexts: map[string]cliconfig.Context{}}
	if err := f.Set(name, ctx); err != nil {
		t.Fatal(err)
	}
	if err := f.Use(name); err != nil {
		t.Fatal(err)
	}
	if err := cliconfig.Save(f); err != nil {
		t.Fatal(err)
	}
}

func setupAWSConfigFile(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(path, []byte(`[profile flag-profile]
region = us-west-1
[profile env-profile]
region = ap-south-1
[profile ctx-profile]
region = eu-central-1
`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AWS_CONFIG_FILE", path)
	return path
}

// Verifies Load applies the context's region (the SDK config carries it)
// and does not error when no AWS credentials are present (no API call made).
func TestLoadAppliesContextRegion(t *testing.T) {
	setupContext(t, "prod", cliconfig.Context{Cluster: "x", Region: "eu-west-1"})

	cfg, err := Load(context.Background(), nil)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Region != "eu-west-1" {
		t.Errorf("cfg.Region = %q, want eu-west-1", cfg.Region)
	}
}

// An AWS env var that names a different region or profile than the active
// context is an error. Honoring it would split the context: the context's
// cluster and its other half would run in an account or region the context
// was never saved for.
func TestLoadEnvConflictingWithContextFails(t *testing.T) {
	for _, tc := range []struct {
		name    string
		ctx     cliconfig.Context
		env     map[string]string
		wantErr string
	}{
		{"AWS_REGION", cliconfig.Context{Cluster: "x", Region: "eu-west-1"},
			map[string]string{"AWS_REGION": "ap-south-1"},
			`AWS_REGION=ap-south-1 conflicts with region "eu-west-1" of the active context "prod"`},
		{"AWS_DEFAULT_REGION", cliconfig.Context{Cluster: "x", Region: "eu-west-1"},
			map[string]string{"AWS_DEFAULT_REGION": "ap-south-1"},
			`AWS_DEFAULT_REGION=ap-south-1 conflicts`},
		{"AWS_PROFILE", cliconfig.Context{Cluster: "x", Region: "eu-west-1", Profile: "ctx-profile"},
			map[string]string{"AWS_PROFILE": "env-profile"},
			`AWS_PROFILE=env-profile conflicts with profile "ctx-profile" of the active context "prod"`},
		{"AWS_DEFAULT_PROFILE", cliconfig.Context{Cluster: "x", Profile: "ctx-profile"},
			map[string]string{"AWS_DEFAULT_PROFILE": "env-profile"},
			`AWS_DEFAULT_PROFILE=env-profile conflicts`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setupContext(t, "prod", tc.ctx)
			setupAWSConfigFile(t)
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			_, err := Load(context.Background(), nil)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("Load() error = %v, want it to contain %q", err, tc.wantErr)
			}
		})
	}
}

// Env vars that agree with the context, or set what the context leaves
// empty, are not a conflict. A flag for the conflicting setting also settles
// it: the flag wins over both.
func TestLoadEnvWithoutConflict(t *testing.T) {
	setupContext(t, "prod", cliconfig.Context{Cluster: "x", Region: "eu-west-1"})
	setupAWSConfigFile(t)
	t.Setenv("AWS_REGION", "eu-west-1")
	t.Setenv("AWS_PROFILE", "env-profile") // the context sets no profile
	cfg, err := Load(context.Background(), nil)
	if err != nil || cfg.Region != "eu-west-1" {
		t.Fatalf("Load() = %q, %v; want eu-west-1 and no error", cfg.Region, err)
	}

	t.Setenv("AWS_REGION", "ap-south-1")
	cmd := newParsedCommand(t,
		[]cli.Flag{&cli.StringFlag{Name: "region"}, &cli.StringFlag{Name: "profile"}},
		"--region", "us-west-1")
	cfg, err = Load(context.Background(), cmd)
	if err != nil || cfg.Region != "us-west-1" {
		t.Fatalf("Load(--region) = %q, %v; want us-west-1 and no error", cfg.Region, err)
	}
}

func TestFlagOrEmpty(t *testing.T) {
	if got := flagOrEmpty(nil, "region"); got != "" {
		t.Fatalf("flagOrEmpty(nil) = %q, want empty", got)
	}

	cmd := newParsedCommand(t,
		[]cli.Flag{&cli.StringFlag{Name: "region", Value: "us-east-2"}})
	if got := flagOrEmpty(cmd, "region"); got != "us-east-2" {
		t.Fatalf("flagOrEmpty() = %q, want us-east-2", got)
	}
}

// A global --region given before a subcommand that declares its own
// (unset) repeatable --region must still reach the AWS config:
// `refresh --region eu-west-1 status` used to fall back to the default region.
func TestFlagOrEmptyFallsBackToShadowedGlobalFlag(t *testing.T) {
	var captured *cli.Command
	newRoot := func() *cli.Command {
		return &cli.Command{
			Name:  "refresh",
			Flags: []cli.Flag{&cli.StringFlag{Name: "region"}},
			Commands: []*cli.Command{{
				Name:  "status",
				Flags: []cli.Flag{&cli.StringSliceFlag{Name: "region", Aliases: []string{"r"}}},
				Action: func(_ context.Context, c *cli.Command) error {
					captured = c
					return nil
				},
			}},
		}
	}
	for _, tc := range []struct {
		argv []string
		want string
		vals []string
	}{
		{[]string{"refresh", "--region", "eu-west-1", "status"}, "eu-west-1", []string{"eu-west-1"}},
		{[]string{"refresh", "status", "-r", "us-west-2", "-r", "ap-south-1"}, "us-west-2", []string{"us-west-2", "ap-south-1"}},
		{[]string{"refresh", "--region", "eu-west-1", "status", "-r", "us-west-2"}, "us-west-2", []string{"us-west-2"}},
		{[]string{"refresh", "status"}, "", nil},
	} {
		if err := newRoot().Run(context.Background(), tc.argv); err != nil {
			t.Fatal(err)
		}
		if got := flagOrEmpty(captured, "region"); got != tc.want {
			t.Errorf("%v: flagOrEmpty = %q, want %q", tc.argv, got, tc.want)
		}
		if got := SetFlagValues(captured, "region"); !slices.Equal(got, tc.vals) {
			t.Errorf("%v: SetFlagValues = %v, want %v", tc.argv, got, tc.vals)
		}
	}
}

// A REFRESH_CONTEXT that names no saved context fails Load before any AWS
// call instead of silently using the saved current context.
func TestLoadUnknownRefreshContextFails(t *testing.T) {
	setupContext(t, "prod", cliconfig.Context{Cluster: "p", Region: "eu-west-1"})
	t.Setenv("REFRESH_CONTEXT", "prdo")
	_, err := Load(context.Background(), nil)
	if err == nil || !strings.Contains(err.Error(), `unknown context "prdo" from REFRESH_CONTEXT`) {
		t.Fatalf("Load() error = %v, want an unknown-context error", err)
	}
}

func TestFlagOrEmptyIgnoresEmptyStringSliceFlag(t *testing.T) {
	cmd := newParsedCommand(t,
		[]cli.Flag{&cli.StringSliceFlag{Name: "region"}})

	if got := flagOrEmpty(cmd, "region"); got != "" {
		t.Fatalf("flagOrEmpty() = %q, want empty", got)
	}
}

func TestFlagOrEmptyReadsFirstStringSliceFlagValue(t *testing.T) {
	cmd := newParsedCommand(t,
		[]cli.Flag{&cli.StringSliceFlag{Name: "region"}},
		"--region", "us-west-2", "--region", "us-east-1")

	if got := flagOrEmpty(cmd, "region"); got != "us-west-2" {
		t.Fatalf("flagOrEmpty() = %q, want us-west-2", got)
	}
}

// A context file that exists but cannot be read or parsed is an error, not
// "no context": ignoring it would drop the saved cluster, region, and
// profile and silently target whatever the AWS defaults point at.
func TestLoadUnreadableContextFileFails(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, path string)
	}{
		{"directory", func(t *testing.T, path string) {
			t.Helper()
			if err := os.Mkdir(path, 0o755); err != nil {
				t.Fatal(err)
			}
		}},
		{"corrupt", func(t *testing.T, path string) {
			t.Helper()
			if err := os.WriteFile(path, []byte("current: [unclosed"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("REFRESH_CONFIG_HOME", dir)
			t.Setenv("REFRESH_CONTEXT", "")
			path := filepath.Join(dir, "context.yaml")
			tc.setup(t, path)
			if _, err := Load(context.Background(), nil); err == nil || !strings.Contains(err.Error(), path) {
				t.Fatalf("Load() error = %v, want an error naming %s", err, path)
			}
		})
	}
}

func TestLoadCLIFlagsOverrideContext(t *testing.T) {
	setupContext(t, "prod", cliconfig.Context{Cluster: "x", Region: "eu-west-1"})
	setupAWSConfigFile(t)

	cmd := newParsedCommand(t,
		[]cli.Flag{&cli.StringFlag{Name: "region"}, &cli.StringFlag{Name: "profile"}},
		"--region", "us-west-1", "--profile", "flag-profile")

	cfg, err := Load(context.Background(), cmd)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Region != "us-west-1" {
		t.Fatalf("cfg.Region = %q, want us-west-1", cfg.Region)
	}
}

func TestLoadProfileFlagWinsOverAWSProfileEnv(t *testing.T) {
	setupContext(t, "prod", cliconfig.Context{Cluster: "x"})
	setupAWSConfigFile(t)
	t.Setenv("AWS_PROFILE", "env-profile")

	cmd := newParsedCommand(t,
		[]cli.Flag{&cli.StringFlag{Name: "region"}, &cli.StringFlag{Name: "profile"}},
		"--profile", "flag-profile")

	cfg, err := Load(context.Background(), cmd)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Region != "us-west-1" {
		t.Fatalf("cfg.Region = %q, want flag profile region us-west-1", cfg.Region)
	}
}

func TestLoadUsesContextProfile(t *testing.T) {
	setupContext(t, "prod", cliconfig.Context{Cluster: "x", Profile: "ctx-profile"})
	setupAWSConfigFile(t)

	cfg, err := Load(context.Background(), nil)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Region != "eu-central-1" {
		t.Fatalf("cfg.Region = %q, want context profile region eu-central-1", cfg.Region)
	}
}
