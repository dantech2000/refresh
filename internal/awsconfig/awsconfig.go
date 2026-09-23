// Package awsconfig wraps aws/config.LoadDefaultConfig with the CLI's
// context resolution, so every command transparently honors the active
// `refresh use` selection (region/profile) without each call site
// re-implementing the chain.
package awsconfig

import (
	"context"
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
//  2. Standard AWS env vars (AWS_PROFILE, AWS_REGION) — handled by SDK
//  3. The active refresh context (from cliconfig)
//  4. AWS SDK defaults (~/.aws/config, IMDS, etc.)
//
// CLI-supplied values always win so the user can override the active context
// for a single invocation.
func Load(ctx context.Context, cmd *cli.Command) (aws.Config, error) {
	var opts []func(*config.LoadOptions) error

	profile := flagOrEmpty(cmd, "profile")
	region := flagOrEmpty(cmd, "region")
	profileFromFlag := profile != ""
	regionFromFlag := region != ""

	// Resolve the context even when both flags are set: a mistyped
	// REFRESH_CONTEXT must fail every command the same way.
	active, ok, err := activeContext()
	if err != nil {
		return aws.Config{}, err
	}
	if profile == "" || region == "" {
		if ok {
			if profile == "" && active.Profile != "" {
				profile = active.Profile
			}
			if region == "" && active.Region != "" {
				region = active.Region
			}
		}
	}

	// Flag-derived values always win. Context-derived values must NOT shadow
	// explicit AWS_PROFILE/AWS_REGION env vars (the SDK resolves those itself):
	// the documented precedence is flags > env vars > refresh context > SDK
	// defaults, and silently overriding AWS_PROFILE with a saved context could
	// point a mutating command at the wrong account.
	if profile != "" && (profileFromFlag || os.Getenv("AWS_PROFILE") == "") {
		opts = append(opts, config.WithSharedConfigProfile(profile))
	}
	if region != "" && (regionFromFlag || (os.Getenv("AWS_REGION") == "" && os.Getenv("AWS_DEFAULT_REGION") == "")) {
		opts = append(opts, config.WithRegion(region))
	}

	return config.LoadDefaultConfig(ctx, opts...)
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
			var out []string
			switch v := f.Get().(type) {
			case string:
				out = append(out, v)
			case []string:
				out = append(out, v...)
			}
			vals := make([]string, 0, len(out))
			for _, s := range out {
				if s = strings.TrimSpace(s); s != "" {
					vals = append(vals, s)
				}
			}
			if len(vals) > 0 {
				return vals
			}
		}
	}
	return nil
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

// activeContext returns the active refresh context. An unreadable context
// file counts as no context; a REFRESH_CONTEXT naming no saved context is an
// error, so a typo never silently targets the saved current context.
func activeContext() (cliconfig.Context, bool, error) {
	f, err := cliconfig.Load()
	if err != nil {
		return cliconfig.Context{}, false, nil //nolint:nilerr // an unreadable context file counts as no context, as before
	}
	_, ctx, ok, err := f.Active()
	return ctx, ok, err
}
