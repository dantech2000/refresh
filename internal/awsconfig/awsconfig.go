// Package awsconfig wraps aws/config.LoadDefaultConfig with the CLI's
// context resolution, so every command transparently honors the active
// `refresh use` selection (region/profile) without each call site
// re-implementing the chain.
package awsconfig

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/urfave/cli/v3"

	"github.com/dantech2000/refresh/internal/cliconfig"
	"github.com/dantech2000/refresh/internal/common"
	appconfig "github.com/dantech2000/refresh/internal/config"
)

// Load returns an aws.Config with profile/region resolved from (in order):
//
//  1. CLI flags --profile / --region (if cmd is non-nil and they are set)
//  2. Standard AWS env vars (AWS_PROFILE, AWS_DEFAULT_PROFILE, AWS_REGION,
//     AWS_DEFAULT_REGION), read by the SDK
//  3. The active refresh context (from cliconfig)
//  4. AWS SDK defaults (~/.aws/config, IMDS, etc.)
//
// CLI-supplied values always win so the user can override the active context
// for a single invocation. The context applies as a unit: an env var may set
// what the context leaves empty, but one that names a different profile or
// region than the context is an error (see envConflict), so the context's
// cluster never runs with half of its settings. An unreadable context file
// and an unknown REFRESH_CONTEXT are errors too.
func Load(ctx context.Context, cmd *cli.Command) (aws.Config, error) {
	// The SDK's own client (so AWS_CA_BUNDLE and defaults modes still
	// apply), with a short dial in region sweeps: an unreachable regional
	// endpoint fails a sweep in seconds, not after the SDK's 30s dial on
	// every attempt. Other calls keep the 30s.
	opts := []func(*config.LoadOptions) error{config.WithHTTPClient(awshttp.NewBuildableClient().WithTransportOptions(func(tr *http.Transport) {
		tr.DialContext = sweepDial(tr.DialContext, appconfig.SweepDialTimeout)
	}))}
	profile, region, err := resolve(cmd)
	if err != nil {
		return aws.Config{}, err
	}
	// A flag value, or a context value that no env var contradicts.
	if profile != "" {
		opts = append(opts, config.WithSharedConfigProfile(profile))
	}
	if region != "" {
		opts = append(opts, config.WithRegion(region))
	}

	cfg, err := config.LoadDefaultConfig(ctx, opts...)
	var missing config.SharedConfigProfileNotExistError
	if errors.As(err, &missing) {
		return cfg, profileNotFound(cmd, missing.Profile, err)
	}
	return cfg, err
}

// profileNotFound explains a profile that is in neither AWS file: where the
// name came from, the profiles that are there, and how to create it.
func profileNotFound(cmd *cli.Command, name string, err error) error {
	from := "AWS_PROFILE"
	switch {
	case flagOrEmpty(cmd, "profile") == name:
		from = "--profile"
	case strings.TrimSpace(os.Getenv("AWS_PROFILE")) == name:
	case strings.TrimSpace(os.Getenv("AWS_DEFAULT_PROFILE")) == name:
		from = "AWS_DEFAULT_PROFILE"
	default:
		from = "the active refresh context"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "AWS profile %q not found\nCause: %s names it, and it is in neither %s nor %s", name, from, tildePath(ConfigFile()), tildePath(CredentialsFile()))
	if ps := Profiles(); len(ps) > 0 {
		b.WriteString("\n\nProfiles there:")
		for _, p := range ps {
			if p.SSO {
				fmt.Fprintf(&b, "\n  %-28s  SSO", p.Name)
			} else {
				fmt.Fprintf(&b, "\n  %s", p.Name)
			}
		}
	}
	fmt.Fprintf(&b, "\n\nCreate it:\n  aws configure sso --profile %[1]s    an SSO profile (IAM Identity Center)\n  aws configure --profile %[1]s        an access key", name)
	return &profileError{msg: b.String(), err: err}
}

// tildePath shows a path under the home directory as ~/...
func tildePath(p string) string {
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		if rest, ok := strings.CutPrefix(p, home+string(os.PathSeparator)); ok {
			return "~/" + rest
		}
	}
	return p
}

// profileError carries the help text and still unwraps to the SDK error.
type profileError struct {
	msg string
	err error
}

func (e *profileError) Error() string { return e.msg }
func (e *profileError) Unwrap() error { return e.err }

// sweepDial bounds dial by timeout when the context is a region sweep's
// (common.FailFastOnDial), and leaves every other dial as it is.
func sweepDial(dial func(ctx context.Context, network, addr string) (net.Conn, error), timeout time.Duration) func(ctx context.Context, network, addr string) (net.Conn, error) {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		if common.IsFailFastOnDial(ctx) {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, timeout)
			defer cancel()
		}
		return dial(ctx, network, addr)
	}
}

// resolve is the profile and region Load gives the SDK itself (the flags,
// else the active context), after checking the context against the env
// vars. "" leaves the setting to the SDK's own chain (env vars, defaults).
func resolve(cmd *cli.Command) (profile, region string, err error) {
	profile = flagOrEmpty(cmd, "profile")
	region = flagOrEmpty(cmd, "region")

	// Resolve the context even when both flags are set: a mistyped
	// REFRESH_CONTEXT or a corrupt context file must fail every command the
	// same way.
	name, active, ok, err := activeContext()
	if err != nil {
		return "", "", err
	}
	if ok {
		if profile == "" {
			if err := envConflict(name, "profile", active.Profile, "AWS_PROFILE", "AWS_DEFAULT_PROFILE"); err != nil {
				return "", "", err
			}
			profile = active.Profile
		}
		if region == "" {
			if err := envConflict(name, "region", active.Region, "AWS_REGION", "AWS_DEFAULT_REGION"); err != nil {
				return "", "", err
			}
			region = active.Region
		}
	}
	return profile, region, nil
}

// EffectiveProfile is the AWS profile Load uses, by the same rules: the
// --profile flag, else the active refresh context's profile (explicit:
// Load gives it to the SDK), else AWS_PROFILE, else AWS_DEFAULT_PROFILE
// (not explicit: the SDK reads them, and exported access keys win over
// them); "" when none is set.
//
// A caller that shows a command to run elsewhere adds --profile only when
// explicit: the command then reaches the same account. A profile from the
// environment is left to the environment, as the UI itself used it.
func EffectiveProfile(cmd *cli.Command) (profile string, explicit bool, err error) {
	profile, _, err = resolve(cmd)
	if err != nil || profile != "" {
		return profile, profile != "", err
	}
	for _, env := range []string{"AWS_PROFILE", "AWS_DEFAULT_PROFILE"} {
		if v := strings.TrimSpace(os.Getenv(env)); v != "" {
			return v, false, nil
		}
	}
	return "", false, nil
}

// envConflict returns an error when the SDK would read setting from an env
// var whose value differs from ctxValue, the value the active context saved.
// envVars are in the SDK's order: the first non-empty one is the one it
// reads. Letting the env var win would split the context (its cluster and
// the other setting would run in an account or region it was not saved for);
// letting the context win would ignore an explicit env var. So neither wins
// silently.
func envConflict(ctxName, setting, ctxValue string, envVars ...string) error {
	if ctxValue == "" {
		return nil
	}
	for _, env := range envVars {
		v := strings.TrimSpace(os.Getenv(env))
		if v == "" {
			continue
		}
		if v == ctxValue {
			return nil
		}
		return fmt.Errorf("%s=%s conflicts with %s %q of the active context %q; pass --%s for this command, unset %s, or switch contexts",
			env, v, setting, ctxValue, ctxName, setting, env)
	}
	return nil
}

// SetFlagValues returns the values of the nearest flag called name that was
// set, searching cmd and then its ancestors. urfave/cli resolves a name to
// the nearest declaration even when that one is unset, so a subcommand's own
// repeatable --region would hide a global `refresh --region X` given before
// the subcommand. A string flag yields one value; a string slice flag yields
// its non-empty values. It returns nil when no command in the lineage set the
// flag.
func SetFlagValues(cmd *cli.Command, name string) []string {
	if cmd == nil {
		return nil
	}
	for _, c := range cmd.Lineage() {
		for _, f := range c.Flags {
			if !slices.Contains(f.Names(), name) || !f.IsSet() {
				continue
			}
			if vals := FlagValues(f); len(vals) > 0 {
				return vals
			}
		}
	}
	return nil
}

// FlagValues returns the trimmed, non-empty values of one string or string
// slice flag, or nil when it has none.
func FlagValues(f cli.Flag) []string {
	var out []string
	switch v := f.Get().(type) {
	case string:
		out = append(out, v)
	case []string:
		out = append(out, v...)
	}
	var vals []string
	for _, s := range out {
		if s = strings.TrimSpace(s); s != "" {
			vals = append(vals, s)
		}
	}
	return vals
}

func flagOrEmpty(cmd *cli.Command, name string) string {
	if cmd == nil {
		return ""
	}
	if vals := SetFlagValues(cmd, name); len(vals) > 0 {
		return vals[0]
	}
	if value := strings.TrimSpace(cmd.String(name)); value != "" {
		return value
	}
	// String() returns "" for slice-typed flags (e.g. cluster list's
	// repeatable --region); fall back to the first slice element.
	if values := cmd.StringSlice(name); len(values) > 0 {
		return strings.TrimSpace(values[0])
	}
	return ""
}

// activeContext returns the active refresh context and its name. A missing
// context file means no context. A file that cannot be read or parsed is an
// error, and so is a REFRESH_CONTEXT naming no saved context: either would
// otherwise silently drop the user's chosen target.
func activeContext() (string, cliconfig.Context, bool, error) {
	f, err := cliconfig.Load()
	if err != nil {
		return "", cliconfig.Context{}, false, err
	}
	return f.Active()
}
