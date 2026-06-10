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
	"github.com/urfave/cli/v2"

	"github.com/dantech2000/refresh/internal/cliconfig"
)

// Load returns an aws.Config with profile/region resolved from (in order):
//
//  1. CLI flags --profile / --region (if c is non-nil and they are set)
//  2. Standard AWS env vars (AWS_PROFILE, AWS_REGION) — handled by SDK
//  3. The active refresh context (from cliconfig)
//  4. AWS SDK defaults (~/.aws/config, IMDS, etc.)
//
// CLI-supplied values always win so the user can override the active context
// for a single invocation.
func Load(ctx context.Context, c *cli.Context) (aws.Config, error) {
	var opts []func(*config.LoadOptions) error

	profile := flagOrEmpty(c, "profile")
	region := flagOrEmpty(c, "region")
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

func flagOrEmpty(c *cli.Context, name string) string {
	if c == nil {
		return ""
	}
	value := strings.TrimSpace(c.String(name))
	if strings.HasPrefix(value, "[") && strings.HasSuffix(value, "]") {
		values := strings.Fields(strings.Trim(value, "[]"))
		if len(values) == 0 {
			return ""
		}
		return values[0]
	}
	return value
}

func activeContext() (cliconfig.Context, bool) {
	f, err := cliconfig.Load()
	if err != nil {
		return cliconfig.Context{}, false
	}
	_, ctx, ok := f.Active()
	return ctx, ok
}

// ActiveClusterName returns the cluster name from the active context, if any.
// Used by ClusterName resolution as a fallback before prompting.
func ActiveClusterName() string {
	if ctx, ok := activeContext(); ok {
		return ctx.Cluster
	}
	return ""
}
