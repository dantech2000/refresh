package cmdutil

import (
	"context"
	"os"
	"strings"
	"sync"

	"github.com/dantech2000/refresh/internal/awsx"
	"github.com/dantech2000/refresh/internal/contexts"
)

// NewAWS returns the function for Factory.AWS of a real run. The first call
// loads the AWS configuration from the global flags of f, the environment,
// and the active refresh context, resolves the credentials, and builds the
// provider. Later calls return the same provider. A failed call is not kept:
// the next call tries again.
//
// Loading runs under the --timeout budget even when ctx has a later deadline
// or none, so a credential source that stalls cannot hang a command that
// waits for hours.
func NewAWS(f *Factory) func(ctx context.Context) (*awsx.Provider, error) {
	var (
		mu       sync.Mutex
		provider *awsx.Provider
	)
	return func(ctx context.Context) (*awsx.Provider, error) {
		mu.Lock()
		defer mu.Unlock()
		if provider != nil {
			return provider, nil
		}
		opts, err := awsOptions(f)
		if err != nil {
			return nil, err
		}
		if f.Global.Timeout > 0 {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, f.Global.Timeout)
			defer cancel()
		}
		cfg, err := awsx.Load(ctx, opts)
		if err != nil {
			return nil, err
		}
		provider = awsx.NewProvider(cfg)
		return provider, nil
	}
}

// awsOptions collects what decides the AWS profile and region of the run.
// It reads the context store also when the flags set both: a REFRESH_CONTEXT
// with a typing error, or a store that cannot be read, fails every command
// in the same way.
func awsOptions(f *Factory) (awsx.Options, error) {
	opts := awsx.Options{Profile: f.Global.Profile, MaxConcurrency: f.Global.MaxConcurrency}
	// With several --region values the first one is the home region. A
	// sweep reads the whole list.
	for _, r := range f.Global.Regions {
		if r = strings.TrimSpace(r); r != "" {
			opts.Region = r
			break
		}
	}

	store, err := f.Contexts()
	if err != nil {
		return awsx.Options{}, err
	}
	file, err := store.Load()
	if err != nil {
		return awsx.Options{}, err
	}
	name, active, ok, err := file.Active(os.Getenv(contexts.EnvContext))
	if err != nil {
		return awsx.Options{}, err
	}
	if ok {
		opts.Context = &awsx.Context{Name: name, Profile: active.Profile, Region: active.Region}
	}
	return opts, nil
}
