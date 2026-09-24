// Package awsconfig wraps aws/config.LoadDefaultConfig with the CLI's
// context resolution, so every command transparently honors the active
// `refresh use` selection (region/profile) without each call site
// re-implementing the chain.
package awsconfig

import (
	"context"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/urfave/cli/v3"

	"github.com/dantech2000/refresh/internal/cliconfig"
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
	var opts []func(*config.LoadOptions) error

	profile := flagOrEmpty(cmd, "profile")
	region := flagOrEmpty(cmd, "region")

	// Resolve the context even when both flags are set: a mistyped
	// REFRESH_CONTEXT or a corrupt context file must fail every command the
	// same way.
	name, active, ok, err := activeContext()
	if err != nil {
		return aws.Config{}, err
	}
	if ok {
		if profile == "" {
			if err := envConflict(name, "profile", active.Profile, "AWS_PROFILE", "AWS_DEFAULT_PROFILE"); err != nil {
				return aws.Config{}, err
			}
			profile = active.Profile
		}
		if region == "" {
			if err := envConflict(name, "region", active.Region, "AWS_REGION", "AWS_DEFAULT_REGION"); err != nil {
				return aws.Config{}, err
			}
			region = active.Region
		}
	}

	// A flag value, or a context value that no env var contradicts.
	if profile != "" {
		opts = append(opts, config.WithSharedConfigProfile(profile))
	}
	if region != "" {
		opts = append(opts, config.WithRegion(region))
	}

	return config.LoadDefaultConfig(ctx, opts...)
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
