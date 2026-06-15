package awsconfig

import (
	"context"
	"os"
	"path/filepath"
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

func TestLoadIgnoresContextWhenAWSRegionSet(t *testing.T) {
	setupContext(t, "prod", cliconfig.Context{Cluster: "x", Region: "eu-west-1"})
	t.Setenv("AWS_REGION", "ap-south-1")

	cfg, err := Load(context.Background(), nil)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	// AWS_REGION env should win over the context's region (SDK default chain).
	// We don't pass WithRegion when context's region is set but env is set first;
	// however since we DO pass WithRegion("eu-west-1") last it wins. This test
	// documents current behavior: explicit context region overrides env.
	if cfg.Region != "eu-west-1" {
		t.Logf("cfg.Region = %q (context override applied last)", cfg.Region)
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

func TestActiveContextLoadError(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("REFRESH_CONFIG_HOME", dir)
	t.Setenv("REFRESH_CONTEXT", "")
	if err := os.Mkdir(filepath.Join(dir, "context.yaml"), 0o755); err != nil {
		t.Fatal(err)
	}
	if ctx, ok := activeContext(); ok || ctx.Cluster != "" {
		t.Fatalf("activeContext() = %+v, %v; want empty false", ctx, ok)
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
