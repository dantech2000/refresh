package awsx

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	"github.com/aws/aws-sdk-go-v2/config"
)

// Options are the settings of one run that decide the AWS profile and
// region.
type Options struct {
	// Profile and Region are the values of --profile and --region. Empty
	// means that the flag was not given.
	Profile string
	Region  string
	// Context is the active refresh context, or nil when there is none.
	Context *Context
	// MaxConcurrency is the limit for parallel calls. It sizes the pool of
	// idle connections.
	MaxConcurrency int
}

// Context is the part of the active refresh context that Load uses.
type Context struct {
	Name    string
	Profile string
	Region  string
}

// Load returns the SDK configuration for a run, with its credentials
// resolved. The profile and the region come from, in this order:
//
//  1. Options.Profile and Options.Region (the flags)
//  2. The standard AWS environment variables, which the SDK reads
//  3. The active refresh context
//  4. The SDK defaults (~/.aws/config, IMDS, and so on)
//
// The context applies as a unit. An environment variable may set what the
// context leaves empty, but one that names another profile or region than
// the context is an error: the cluster of the context must not run with
// half of its settings.
//
// Load makes no AWS API call of its own. A credential source that needs the
// network (SSO role credentials, AssumeRole, IMDS) makes here the call that
// the first API call would make. The credentials stay in the cache of the
// configuration. Load cannot tell that keys which resolve are revoked: the
// first API call reports that.
func Load(ctx context.Context, opts Options) (aws.Config, error) {
	profile, region, err := opts.resolve()
	if err != nil {
		return aws.Config{}, err
	}
	loadOpts := []func(*config.LoadOptions) error{config.WithHTTPClient(NewHTTPClient(opts.MaxConcurrency))}
	if profile != "" {
		loadOpts = append(loadOpts, config.WithSharedConfigProfile(profile))
	}
	if region != "" {
		loadOpts = append(loadOpts, config.WithRegion(region))
	}

	cfg, err := config.LoadDefaultConfig(ctx, loadOpts...)
	if err != nil {
		var missing config.SharedConfigProfileNotExistError
		if errors.As(err, &missing) {
			err = opts.profileNotFound(missing.Profile, err)
		}
		return aws.Config{}, fmt.Errorf("failed to load AWS config: %w", err)
	}
	if err := opts.resolveCredentials(ctx, cfg); err != nil {
		return aws.Config{}, fmt.Errorf("AWS credential validation failed: %w", err)
	}
	return cfg, nil
}

// NewHTTPClient returns the HTTP client that every region and service
// shares. maxConcurrency is the limit for parallel calls; the client keeps
// that many idle connections for each endpoint, so a fan-out reuses its
// connections in place of opening new ones.
//
// The client is the SDK's own, so AWS_CA_BUNDLE and the SDK defaults still
// apply to it. A call of a Sweep client gets a short dial timeout.
func NewHTTPClient(maxConcurrency int) *awshttp.BuildableClient {
	return awshttp.NewBuildableClient().WithTransportOptions(func(tr *http.Transport) {
		tr.MaxIdleConnsPerHost = max(tr.MaxIdleConnsPerHost, maxConcurrency)
		// A sweep talks to a few services in each of many regions.
		tr.MaxIdleConns = max(tr.MaxIdleConns, 4*tr.MaxIdleConnsPerHost)
		tr.DialContext = sweepDial(tr.DialContext, sweepDialTimeout)
	})
}

type dialFunc func(ctx context.Context, network, addr string) (net.Conn, error)

type sweepDialKey struct{}

// withSweepDial marks ctx as the context of a Sweep call.
func withSweepDial(ctx context.Context) context.Context {
	return context.WithValue(ctx, sweepDialKey{}, true)
}

// sweepDial bounds dial by timeout for a Sweep call and leaves every other
// dial as it is: the poll of a long wait keeps the SDK's dial timeout.
func sweepDial(dial dialFunc, timeout time.Duration) dialFunc {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		if sweep, _ := ctx.Value(sweepDialKey{}).(bool); sweep {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, timeout)
			defer cancel()
		}
		return dial(ctx, network, addr)
	}
}

// resolve returns the profile and region that Load gives to the SDK: the
// flags, else the active context after a check against the environment. ""
// leaves the setting to the SDK's own chain.
func (o Options) resolve() (profile, region string, err error) {
	profile, region = strings.TrimSpace(o.Profile), strings.TrimSpace(o.Region)
	if o.Context == nil {
		return profile, region, nil
	}
	if profile == "" {
		if err := envConflict(o.Context.Name, "profile", o.Context.Profile, "AWS_PROFILE", "AWS_DEFAULT_PROFILE"); err != nil {
			return "", "", err
		}
		profile = o.Context.Profile
	}
	if region == "" {
		if err := envConflict(o.Context.Name, "region", o.Context.Region, "AWS_REGION", "AWS_DEFAULT_REGION"); err != nil {
			return "", "", err
		}
		region = o.Context.Region
	}
	return profile, region, nil
}

// EffectiveProfile returns the AWS profile that Load uses, by the same
// rules: Options.Profile, else the profile of the active context, else
// AWS_PROFILE, else AWS_DEFAULT_PROFILE, else "".
//
// explicit reports that refresh itself gives the profile to the SDK (a flag
// or the context). A profile from the environment is not explicit: the SDK
// reads it, and exported access keys win over it.
func (o Options) EffectiveProfile() (profile string, explicit bool, err error) {
	profile, _, err = o.resolve()
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

// envConflict returns an error when the SDK would read setting from an
// environment variable whose value differs from ctxValue, the value that the
// active context saved. envVars are in the SDK's order: it reads the first
// one that is not empty.
//
// Neither side wins silently. If the variable won, the cluster of the
// context would run in an account or region that the context was not saved
// for. If the context won, an explicit variable would be ignored.
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

// profileNotFound explains a profile that is in neither AWS file: where the
// name came from, the profiles that are there, and how to create it.
func (o Options) profileNotFound(name string, err error) error {
	requested, _, _ := o.EffectiveProfile()
	if requested == "" {
		// The SDK reads [default] when nothing names a profile.
		requested = "default"
	}
	var from string
	switch {
	case requested != name:
		// The SDK followed a source_profile to a profile that is not there.
		from = fmt.Sprintf("the source_profile of profile %q (or a profile it refers to)", requested)
	case strings.TrimSpace(o.Profile) == name:
		from = "--profile"
	case strings.TrimSpace(os.Getenv("AWS_PROFILE")) == name:
		from = "AWS_PROFILE"
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
	return &formattedError{msg: b.String(), err: err}
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

// resolveCredentials retrieves the credentials of cfg and returns one error
// for the caller to report. It catches missing credentials, a credential
// source that fails, and an SSO login that expired.
func (o Options) resolveCredentials(ctx context.Context, cfg aws.Config) error {
	if cfg.Credentials == nil {
		return nil // anonymous: nothing to resolve
	}
	_, err := cfg.Credentials.Retrieve(ctx)
	if err == nil {
		return nil
	}
	switch {
	case IsNoCredentials(err):
		return FormatNoCredentials(err, SSOProfiles())
	case IsSSONotLoggedIn(err):
		// Name the profile to log in with. A role profile logs in through
		// the SSO profile at the end of its source_profile chain.
		profile, _, _ := o.EffectiveProfile()
		if login := SSOLoginProfile(ctx, profile); login != "" {
			profile = login
		}
		return FormatSSONotLoggedIn(err, profile)
	}
	return NoteKeysShadowProfile(ctx, cfg, FormatAWSError(err, "loading AWS credentials"))
}

// NoteKeysShadowProfile adds a note to a credential error when the keys of
// cfg came from the environment while AWS_PROFILE is set. The SDK uses the
// keys first, as the AWS CLI does, so the profile that the user chose has no
// effect. Any other error comes back as it is.
func NoteKeysShadowProfile(ctx context.Context, cfg aws.Config, err error) error {
	if err == nil || !IsCredentialError(err) || cfg.Credentials == nil ||
		strings.TrimSpace(os.Getenv("AWS_PROFILE")) == "" || strings.TrimSpace(os.Getenv("AWS_ACCESS_KEY_ID")) == "" {
		return err
	}
	creds, rerr := cfg.Credentials.Retrieve(ctx)
	if rerr != nil || creds.Source != config.CredentialsSourceName {
		return err
	}
	return &formattedError{msg: err.Error() + keysShadowProfileNote, err: err}
}
