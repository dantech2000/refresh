package awsconfig

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	"github.com/urfave/cli/v3"

	"github.com/dantech2000/refresh/internal/cliconfig"
	"github.com/dantech2000/refresh/internal/common"
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

// From review: the UI named only --profile and AWS_PROFILE in the commands
// it shows, so a context's profile dropped out of a copied command, which
// then ran in another account. EffectiveProfile follows Load's rules, and
// says whether refresh gave the profile to the SDK itself (explicit).
func TestEffectiveProfile(t *testing.T) {
	flags := []cli.Flag{&cli.StringFlag{Name: "region"}, &cli.StringFlag{Name: "profile"}}
	check := func(name string, cmd *cli.Command, want string, wantExplicit bool) {
		t.Helper()
		got, explicit, err := EffectiveProfile(cmd)
		if err != nil || got != want || explicit != wantExplicit {
			t.Errorf("%s: %q explicit=%v err=%v; want %q explicit=%v", name, got, explicit, err, want, wantExplicit)
		}
	}

	setupContext(t, "prod", cliconfig.Context{Cluster: "x", Profile: "ctx-profile"})
	check("context", newParsedCommand(t, flags), "ctx-profile", true)
	check("flag over context", newParsedCommand(t, flags, "--profile", "flag-profile"), "flag-profile", true)
	t.Setenv("AWS_DEFAULT_PROFILE", "other")
	if _, _, err := EffectiveProfile(newParsedCommand(t, flags)); err == nil {
		t.Error("a context profile an env var contradicts must fail, as Load does")
	}

	setupContext(t, "noprofile", cliconfig.Context{Cluster: "x"})
	t.Setenv("AWS_DEFAULT_PROFILE", "default-env")
	check("AWS_DEFAULT_PROFILE", newParsedCommand(t, flags), "default-env", false)
	t.Setenv("AWS_PROFILE", "env")
	check("AWS_PROFILE", newParsedCommand(t, flags), "env", false)

	t.Setenv("REFRESH_CONTEXT", "missing")
	if _, _, err := EffectiveProfile(newParsedCommand(t, flags, "--profile", "p")); err == nil {
		t.Error("an unknown REFRESH_CONTEXT must fail, as Load does")
	}
}

// #418: in a region sweep (common.FailFastOnDial) a dial is bounded, so an
// unreachable regional endpoint fails in seconds; any other dial keeps the
// caller's deadline (the SDK's 30s). The client stays the SDK's buildable
// one, so AWS_CA_BUNDLE and defaults modes still apply to it.
func TestSweepDialIsBoundedOnlyInSweeps(t *testing.T) {
	hang := func(ctx context.Context, _, _ string) (net.Conn, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	dial := sweepDial(hang, 20*time.Millisecond)

	start := time.Now()
	if _, err := dial(common.FailFastOnDial(context.Background()), "tcp", "eks.me-south-1.amazonaws.com:443"); err == nil {
		t.Fatal("a hanging dial in a sweep returned no error")
	}
	if d := time.Since(start); d > time.Second {
		t.Errorf("a sweep's dial took %v, want about the 20ms bound", d)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start = time.Now()
	_, _ = dial(ctx, "tcp", "eks.us-east-1.amazonaws.com:443")
	if d := time.Since(start); d < 150*time.Millisecond {
		t.Errorf("a dial outside a sweep ended after %v, before the caller's deadline", d)
	}

	setupContext(t, "prod", cliconfig.Context{Cluster: "x", Region: "us-east-1"})
	setupAWSConfigFile(t)
	cfg, err := Load(context.Background(), newParsedCommand(t, []cli.Flag{&cli.StringFlag{Name: "region"}, &cli.StringFlag{Name: "profile"}}))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := cfg.HTTPClient.(*awshttp.BuildableClient); !ok {
		t.Errorf("HTTPClient is %T, want the SDK's buildable client", cfg.HTTPClient)
	}
}
