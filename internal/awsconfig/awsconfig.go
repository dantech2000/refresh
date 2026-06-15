// Package awsconfig wraps aws/config.LoadDefaultConfig with the CLI's
// context resolution, so every command transparently honors the active
// `refresh use` selection (region/profile) without each call site
// re-implementing the chain.
package awsconfig

import (
	"context"
	"os"
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

	if profile == "" || region == "" {
		if active, ok := activeContext(); ok {
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

func flagOrEmpty(cmd *cli.Command, name string) string {
	if cmd == nil {
		return ""
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

func activeContext() (cliconfig.Context, bool) {
	f, err := cliconfig.Load()
	if err != nil {
		return cliconfig.Context{}, false
	}
	_, ctx, ok := f.Active()
	return ctx, ok
}
