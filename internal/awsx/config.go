package awsx

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/retry"
	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/smithy-go/middleware"
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
	return load(ctx, opts, NewHTTPClient(opts.MaxConcurrency), nil)
}

// load is Load with the HTTP client given by the caller, and with the delay
// between the attempts of a credential call when backoff is not nil.
func load(ctx context.Context, opts Options, client *awshttp.BuildableClient, backoff retry.BackoffDelayer) (aws.Config, error) {
	profile, region, err := opts.resolve()
	if err != nil {
		return aws.Config{}, err
	}
	loadOpts := []func(*config.LoadOptions) error{
		config.WithHTTPClient(client),
		// The credential chain builds its own clients from the loaded
		// configuration: STS for a role profile, SSO, and IMDS. They follow
		// the Standard policy too, so no call has a second set of retry
		// rules. Setting the attempt count here keeps AWS_MAX_ATTEMPTS and
		// max_attempts from replacing it.
		config.WithRetryer(func() aws.Retryer { return newRetryer(Standard, backoff) }),
		config.WithRetryMaxAttempts(maxAttempts),
	}
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
			// A formatted error has its own headline.
			return aws.Config{}, opts.profileNotFound(missing.Profile, err)
		}
		return aws.Config{}, fmt.Errorf("failed to load AWS config: %w", err)
	}
	// Before any call: a region name that cannot exist would otherwise
	// fail as an endpoint that does not resolve.
	if err := checkLoadedRegion(cfg.Region, region, opts); err != nil {
		return aws.Config{}, err
	}
	// Freeze the HTTP client. A service client changes a buildable client
	// when a defaults mode is set (AWS_DEFAULTS_MODE): it then makes its
	// own copy with another dialer, which would end the sharing of
	// connections and drop the dial timeout of a sweep. The SDK has
	// already applied AWS_CA_BUNDLE to the client at this point.
	//
	// The clients of the credential chain were built before this point and
	// keep the buildable client: the SDK adds the CA bundle only to a
	// buildable one, so a frozen client cannot go into LoadDefaultConfig.
	// Under a defaults mode they therefore dial on a transport of their
	// own. Those are a few calls for each run.
	if buildable, ok := cfg.HTTPClient.(*awshttp.BuildableClient); ok {
		cfg.HTTPClient = freeze(buildable, defaultStallLimits().bodyIdle)
	}
	cfg.Credentials = markIdentityFailures(cfg.Credentials)
	// An SSO login can expire in the middle of a run. Every client built
	// from this configuration then names the profile to log in with. The
	// profile is looked up once, when the first such failure needs it.
	login := sync.OnceValue(func() string { return opts.ssoLoginProfile(context.WithoutCancel(ctx)) })
	// Keys from the environment resolve also when AWS has revoked them, so
	// only the first API call rejects them. That error then says that the
	// keys were used in place of AWS_PROFILE, as an error of Load does.
	shadow := sync.OnceValue(func() bool { return keysShadowProfile(context.WithoutCancel(ctx), cfg.Credentials) })
	cfg.APIOptions = append(cfg.APIOptions, addCredentialHints(login, shadow))
	// resolveCredentials formats its error, with a headline of its own: a
	// prefix here would put two headlines on the first line.
	if err := opts.resolveCredentials(ctx, cfg, login); err != nil {
		return aws.Config{}, err
	}
	return cfg, nil
}

// checkLoadedRegion checks the shape of the region of the run, and names
// where it came from: the flag, the context, or the AWS configuration.
// given is the region that refresh passed to the SDK, or "".
func checkLoadedRegion(loaded, given string, opts Options) error {
	if loaded == "" {
		return nil
	}
	err := CheckRegion(loaded)
	switch {
	case err == nil:
		return nil
	case given != "" && strings.TrimSpace(opts.Region) == "" && opts.Context != nil:
		return fmt.Errorf("invalid region of the active context %q: %w", opts.Context.Name, err)
	case given != "":
		return fmt.Errorf("invalid --region: %w", err)
	}
	return fmt.Errorf("invalid AWS region from AWS_REGION, AWS_DEFAULT_REGION, or the profile in ~/.aws/config: %w", err)
}

// NewHTTPClient returns the HTTP client that every region and service
// shares. maxConcurrency is the limit for parallel calls; the client keeps
// that many idle connections for each endpoint, so a fan-out reuses its
// connections in place of opening new ones.
//
// The client is the SDK's own, so AWS_CA_BUNDLE and the SDK defaults still
// apply to it. A call of a Sweep client gets a short dial timeout.
//
// A call that gets no answer does not wait for the deadline of the command:
// the client gives up when the response headers take longer than a minute,
// and it closes an HTTP/2 connection that is silent for 30 seconds and then
// does not answer a ping in 15. Both failures are transient, so the retry
// policy of the call applies. Load adds a limit for a response body that
// stops coming.
func NewHTTPClient(maxConcurrency int) *awshttp.BuildableClient {
	return newHTTPClient(maxConcurrency, defaultStallLimits())
}

func newHTTPClient(maxConcurrency int, limits stallLimits) *awshttp.BuildableClient {
	return awshttp.NewBuildableClient().WithTransportOptions(func(tr *http.Transport) {
		tr.ResponseHeaderTimeout = limits.headers
		tr.HTTP2 = &http.HTTP2Config{SendPingTimeout: limits.pingAfter, PingTimeout: limits.pingTimeout}
		tr.MaxIdleConnsPerHost = max(tr.MaxIdleConnsPerHost, maxConcurrency)
		// No limit on the idle connections of all hosts together. A sweep
		// talks to a few services in each of many regions, and every
		// region is a host of its own: a total below regions times the
		// limit for one host would close connections between two waves
		// and open them again.
		tr.MaxIdleConns = 0
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
func (o Options) resolveCredentials(ctx context.Context, cfg aws.Config, login func() string) error {
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
		return FormatSSONotLoggedIn(err, login())
	}
	return NoteKeysShadowProfile(ctx, cfg, FormatAWSError(err, "loading AWS credentials"))
}

// ssoLoginProfile returns the profile to log in with when the SSO login of
// this run is missing or expired. A role profile logs in through the SSO
// profile at the end of its source_profile chain.
func (o Options) ssoLoginProfile(ctx context.Context) string {
	profile, _, _ := o.EffectiveProfile()
	if login := SSOLoginProfile(ctx, profile); login != "" {
		return login
	}
	return profile
}

// addCredentialHints adds a step to each call that marks a credential
// failure with what only Load knows, for FormatAWSError: an SSO login
// failure gets the profile to log in with, and keys that AWS rejects get a
// mark when they came from the environment in place of AWS_PROFILE.
func addCredentialHints(profile func() string, keysShadow func() bool) func(*middleware.Stack) error {
	return func(stack *middleware.Stack) error {
		return stack.Initialize.Add(middleware.InitializeMiddlewareFunc("refresh:CredentialHints",
			func(ctx context.Context, in middleware.InitializeInput, next middleware.InitializeHandler) (middleware.InitializeOutput, middleware.Metadata, error) {
				out, metadata, err := next.HandleInitialize(ctx, in)
				switch {
				case err == nil:
				case IsSSONotLoggedIn(err):
					err = &ssoLoginError{profile: profile(), err: err}
				case IsCredentialError(err) && keysShadow():
					err = &keysShadowError{err: err}
				}
				return out, metadata, err
			}), middleware.Before)
	}
}

// keysShadowProfile reports whether the credentials of provider are keys
// from the environment while AWS_PROFILE is set. The SDK uses the keys
// first, as the AWS CLI does, so the profile that the user chose has no
// effect.
func keysShadowProfile(ctx context.Context, provider aws.CredentialsProvider) bool {
	if provider == nil || strings.TrimSpace(os.Getenv("AWS_PROFILE")) == "" || strings.TrimSpace(os.Getenv("AWS_ACCESS_KEY_ID")) == "" {
		return false
	}
	creds, err := provider.Retrieve(ctx)
	return err == nil && creds.Source == config.CredentialsSourceName
}

// NoteKeysShadowProfile adds a note to a credential error when the keys of
// cfg came from the environment while AWS_PROFILE is set. The SDK uses the
// keys first, as the AWS CLI does, so the profile that the user chose has no
// effect. Any other error comes back as it is.
func NoteKeysShadowProfile(ctx context.Context, cfg aws.Config, err error) error {
	if err == nil || !IsCredentialError(err) || !keysShadowProfile(ctx, cfg.Credentials) {
		return err
	}
	// An error of a client from Load already has the note.
	var marked *keysShadowError
	if errors.As(err, &marked) {
		return err
	}
	return &formattedError{msg: err.Error() + keysShadowProfileNote, err: err}
}
