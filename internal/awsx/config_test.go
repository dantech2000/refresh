package awsx

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/aws/smithy-go"

	"github.com/dantech2000/refresh/internal/awsx/awsxtest"
	"github.com/dantech2000/refresh/internal/fleet/fleettest"
)

// isolateAWS gives a test an AWS environment of its own: a config file with
// three profiles, no credentials file, static keys, and no variable that
// names a profile or a region. Nothing reads the real ~/.aws. Each profile
// has keys of its own: a profile that refresh names beats the keys of the
// environment.
func isolateAWS(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config")
	if err := os.WriteFile(path, []byte(`[profile flag-profile]
region = us-west-1
aws_access_key_id = AKIDPROFILEFAKE
aws_secret_access_key = fake-secret
[profile env-profile]
region = ap-south-1
aws_access_key_id = AKIDPROFILEFAKE
aws_secret_access_key = fake-secret
[profile ctx-profile]
region = eu-central-1
aws_access_key_id = AKIDPROFILEFAKE
aws_secret_access_key = fake-secret
`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", dir)
	t.Setenv("AWS_CONFIG_FILE", path)
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", filepath.Join(dir, "none"))
	t.Setenv("AWS_ACCESS_KEY_ID", "AKIDFAKEFAKEFAKE")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "fake-secret")
	t.Setenv("AWS_SESSION_TOKEN", "")
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")
	for _, name := range []string{"AWS_PROFILE", "AWS_DEFAULT_PROFILE", "AWS_REGION", "AWS_DEFAULT_REGION"} {
		t.Setenv(name, "")
	}
}

// TestLoadPrecedence: the flags win, then the AWS environment variables,
// then the active context, then the profile's own settings. The region of
// the loaded configuration shows which profile and region won.
func TestLoadPrecedence(t *testing.T) {
	tests := []struct {
		name       string
		opts       Options
		env        map[string]string
		wantRegion string
		wantErr    string
	}{
		{name: "nothing set"},
		{name: "region flag", opts: Options{Region: "us-west-2"}, wantRegion: "us-west-2"},
		{name: "profile flag gives the profile's region", opts: Options{Profile: "flag-profile"}, wantRegion: "us-west-1"},
		{
			name:       "region flag beats the profile's region",
			opts:       Options{Profile: "flag-profile", Region: "us-west-2"},
			wantRegion: "us-west-2",
		},
		{name: "AWS_REGION", env: map[string]string{"AWS_REGION": "ap-south-1"}, wantRegion: "ap-south-1"},
		{name: "AWS_PROFILE", env: map[string]string{"AWS_PROFILE": "env-profile"}, wantRegion: "ap-south-1"},
		{
			name:       "region flag beats AWS_REGION",
			opts:       Options{Region: "us-west-2"},
			env:        map[string]string{"AWS_REGION": "ap-south-1"},
			wantRegion: "us-west-2",
		},
		{
			name:       "profile flag beats AWS_PROFILE",
			opts:       Options{Profile: "flag-profile"},
			env:        map[string]string{"AWS_PROFILE": "env-profile"},
			wantRegion: "us-west-1",
		},
		{
			name:       "context region",
			opts:       Options{Context: &Context{Name: "prod", Region: "eu-west-1"}},
			wantRegion: "eu-west-1",
		},
		{
			name:       "context profile",
			opts:       Options{Context: &Context{Name: "prod", Profile: "ctx-profile"}},
			wantRegion: "eu-central-1",
		},
		{
			name:       "flags beat the context",
			opts:       Options{Profile: "flag-profile", Region: "us-west-2", Context: &Context{Name: "prod", Profile: "ctx-profile", Region: "eu-west-1"}},
			wantRegion: "us-west-2",
		},
		{
			name:       "the environment sets what the context leaves empty",
			opts:       Options{Context: &Context{Name: "prod", Region: "eu-west-1"}},
			env:        map[string]string{"AWS_PROFILE": "env-profile"},
			wantRegion: "eu-west-1",
		},
		{
			name:       "an environment value equal to the context is no conflict",
			opts:       Options{Context: &Context{Name: "prod", Region: "eu-west-1"}},
			env:        map[string]string{"AWS_REGION": "eu-west-1"},
			wantRegion: "eu-west-1",
		},
		{
			// A flag for the setting settles the conflict: it beats both.
			name:       "a flag settles a conflict",
			opts:       Options{Region: "us-west-2", Context: &Context{Name: "prod", Region: "eu-west-1"}},
			env:        map[string]string{"AWS_REGION": "ap-south-1"},
			wantRegion: "us-west-2",
		},
		{
			name:    "AWS_REGION conflicts with the context",
			opts:    Options{Context: &Context{Name: "prod", Region: "eu-west-1"}},
			env:     map[string]string{"AWS_REGION": "ap-south-1"},
			wantErr: `AWS_REGION=ap-south-1 conflicts with region "eu-west-1" of the active context "prod"; pass --region for this command, unset AWS_REGION, or switch contexts`,
		},
		{
			name:    "AWS_DEFAULT_REGION conflicts with the context",
			opts:    Options{Context: &Context{Name: "prod", Region: "eu-west-1"}},
			env:     map[string]string{"AWS_DEFAULT_REGION": "ap-south-1"},
			wantErr: `AWS_DEFAULT_REGION=ap-south-1 conflicts with region "eu-west-1" of the active context "prod"`,
		},
		{
			name:    "AWS_PROFILE conflicts with the context",
			opts:    Options{Context: &Context{Name: "prod", Region: "eu-west-1", Profile: "ctx-profile"}},
			env:     map[string]string{"AWS_PROFILE": "env-profile"},
			wantErr: `AWS_PROFILE=env-profile conflicts with profile "ctx-profile" of the active context "prod"`,
		},
		{
			name:    "AWS_DEFAULT_PROFILE conflicts with the context",
			opts:    Options{Context: &Context{Name: "prod", Profile: "ctx-profile"}},
			env:     map[string]string{"AWS_DEFAULT_PROFILE": "env-profile"},
			wantErr: `AWS_DEFAULT_PROFILE=env-profile conflicts with profile "ctx-profile" of the active context "prod"`,
		},
		{
			// The SDK reads AWS_REGION before AWS_DEFAULT_REGION, so the
			// first one decides.
			name:       "the first variable that the SDK reads decides",
			opts:       Options{Context: &Context{Name: "prod", Region: "eu-west-1"}},
			env:        map[string]string{"AWS_REGION": "eu-west-1", "AWS_DEFAULT_REGION": "ap-south-1"},
			wantRegion: "eu-west-1",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			isolateAWS(t)
			for name, value := range tt.env {
				t.Setenv(name, value)
			}

			cfg, err := Load(context.Background(), tt.opts)

			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("Load() error = %v, want one that contains %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Load() error = %v", err)
			}
			if cfg.Region != tt.wantRegion {
				t.Errorf("region = %q, want %q", cfg.Region, tt.wantRegion)
			}
			// A frozen client: no service client can swap its dialer.
			if _, ok := cfg.HTTPClient.(*http.Client); !ok {
				t.Errorf("HTTP client is %T, want a frozen *http.Client", cfg.HTTPClient)
			}
		})
	}
}

func TestEffectiveProfile(t *testing.T) {
	tests := []struct {
		name         string
		opts         Options
		env          map[string]string
		want         string
		wantExplicit bool
		wantErr      bool
	}{
		{name: "nothing"},
		{name: "context", opts: Options{Context: &Context{Name: "prod", Profile: "ctx-profile"}}, want: "ctx-profile", wantExplicit: true},
		{
			name: "flag over context",
			opts: Options{Profile: "flag-profile", Context: &Context{Name: "prod", Profile: "ctx-profile"}},
			want: "flag-profile", wantExplicit: true,
		},
		{
			name:    "context that the environment contradicts",
			opts:    Options{Context: &Context{Name: "prod", Profile: "ctx-profile"}},
			env:     map[string]string{"AWS_DEFAULT_PROFILE": "other"},
			wantErr: true,
		},
		{name: "AWS_DEFAULT_PROFILE", env: map[string]string{"AWS_DEFAULT_PROFILE": "default-env"}, want: "default-env"},
		{
			name: "AWS_PROFILE before AWS_DEFAULT_PROFILE",
			env:  map[string]string{"AWS_DEFAULT_PROFILE": "default-env", "AWS_PROFILE": "env"},
			want: "env",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			isolateAWS(t)
			for name, value := range tt.env {
				t.Setenv(name, value)
			}
			got, explicit, err := tt.opts.EffectiveProfile()
			if (err != nil) != tt.wantErr {
				t.Fatalf("EffectiveProfile() error = %v, want an error: %t", err, tt.wantErr)
			}
			if got != tt.want || explicit != tt.wantExplicit {
				t.Errorf("EffectiveProfile() = %q, explicit %t; want %q, %t", got, explicit, tt.want, tt.wantExplicit)
			}
		})
	}
}

// Load resolves the credentials and makes no API call: the endpoint that
// every AWS call would go to is never dialled.
func TestLoadMakesNoAPICall(t *testing.T) {
	isolateAWS(t)
	// Any request to an AWS API would go here and fail the test.
	t.Setenv("AWS_ENDPOINT_URL", "http://127.0.0.1:1")

	cfg, err := Load(context.Background(), Options{Region: "us-east-1"})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	creds, err := cfg.Credentials.Retrieve(context.Background())
	if err != nil || creds.AccessKeyID != "AKIDFAKEFAKEFAKE" {
		t.Errorf("credentials = %q, %v; want the keys of the environment", creds.AccessKeyID, err)
	}
}

// A credential source that fails is reported by Load, before any command
// work, with the setup help.
func TestLoadReportsCredentialFailure(t *testing.T) {
	isolateAWS(t)
	t.Setenv("AWS_ACCESS_KEY_ID", "")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "")
	path := os.Getenv("AWS_CONFIG_FILE")
	// A profile whose credentials come from a program that fails.
	if err := os.WriteFile(path, []byte("[profile broken]\nregion = us-east-1\ncredential_process = false\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := Load(context.Background(), Options{Profile: "broken"})

	if err == nil {
		t.Fatal("Load() error = nil for a credential source that fails")
	}
	for _, want := range []string{"AWS credential validation failed: ", "AWS credentials not configured or invalid", "aws sso login"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not contain %q:\n%v", want, err)
		}
	}
}

// The note about AWS_ACCESS_KEY_ID hiding AWS_PROFILE appears only when the
// credentials really came from the environment: a profile given with
// --profile or a context wins over the keys.
func TestNoteKeysShadowProfile(t *testing.T) {
	bad := FormatAWSError(&smithy.GenericAPIError{Code: "InvalidClientTokenId", Message: "invalid"}, "x")
	provider := func(source string) aws.Config {
		return aws.Config{Credentials: aws.NewCredentialsCache(aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
			return aws.Credentials{AccessKeyID: "AKIAEXAMPLE", SecretAccessKey: "x", Source: source}, nil
		}))}
	}
	tests := []struct {
		name     string
		cfg      aws.Config
		profile  string // AWS_PROFILE
		err      error
		wantNote bool
	}{
		{name: "keys from the environment", cfg: provider(config.CredentialsSourceName), profile: "prod", err: bad, wantNote: true},
		{name: "keys from the profile", cfg: provider("SharedConfigCredentials: prod"), profile: "prod", err: bad},
		{name: "no AWS_PROFILE", cfg: provider(config.CredentialsSourceName), err: bad},
		{name: "not a credential error", cfg: provider(config.CredentialsSourceName), profile: "prod", err: errors.New("boom")},
		{name: "anonymous", cfg: aws.Config{}, profile: "prod", err: bad},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("AWS_PROFILE", tt.profile)
			t.Setenv("AWS_ACCESS_KEY_ID", "AKIAEXAMPLE")
			got := NoteKeysShadowProfile(context.Background(), tt.cfg, tt.err)
			if has := strings.Contains(got.Error(), "the SDK uses it before AWS_PROFILE"); has != tt.wantNote {
				t.Errorf("note present = %t, want %t:\n%v", has, tt.wantNote, got)
			}
			if !errors.Is(got, tt.err) {
				t.Error("the result does not wrap the error")
			}
		})
	}
	if NoteKeysShadowProfile(context.Background(), aws.Config{}, nil) != nil {
		t.Error("a nil error got a note")
	}
}

// The dial of a Sweep call is bounded, so an endpoint that cannot be reached
// fails in seconds. Any other dial keeps the deadline of the caller.
func TestSweepDialIsBoundedOnlyInSweeps(t *testing.T) {
	hang := func(ctx context.Context, _, _ string) (net.Conn, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	dial := sweepDial(hang, 20*time.Millisecond)

	start := time.Now()
	if _, err := dial(withSweepDial(context.Background()), "tcp", "eks.me-south-1.amazonaws.com:443"); err == nil {
		t.Fatal("a hanging dial in a sweep returned no error")
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("the dial of a sweep took %v, want about the 20ms bound", d)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start = time.Now()
	_, _ = dial(ctx, "tcp", "eks.us-east-1.amazonaws.com:443")
	if d := time.Since(start); d < 150*time.Millisecond {
		t.Errorf("a dial outside a sweep ended after %v, before the deadline of the caller", d)
	}
}

// TestDialFailureAttempts counts the dials of one call to an endpoint that
// refuses connections. Sweep does not try again; Standard does.
func TestDialFailureAttempts(t *testing.T) {
	// A listener that is closed again leaves a port that refuses.
	l, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	endpoint := "http://" + l.Addr().String()
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		policy    Policy
		wantDials int32
		wantSweep bool // the dial ran with the sweep bound
	}{
		{policy: Standard, wantDials: 5},
		{policy: Sweep, wantDials: 1, wantSweep: true},
		{policy: Poll, wantDials: 1},
	}
	for _, tt := range tests {
		t.Run(tt.policy.String(), func(t *testing.T) {
			var dials, sweeps atomic.Int32
			client := NewHTTPClient(8).WithTransportOptions(func(tr *http.Transport) {
				next := tr.DialContext
				tr.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
					dials.Add(1)
					if sweep, _ := ctx.Value(sweepDialKey{}).(bool); sweep {
						sweeps.Add(1)
					}
					return next(ctx, network, addr)
				}
			})
			defer client.GetTransport().CloseIdleConnections()
			cfg := aws.Config{
				Region:       "us-east-1",
				BaseEndpoint: aws.String(endpoint),
				HTTPClient:   client,
				Credentials: aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
					return aws.Credentials{AccessKeyID: "AKIDFAKEFAKEFAKE", SecretAccessKey: "fake"}, nil
				}),
			}
			provider := NewProvider(cfg, func(o *ProviderOptions) { o.Backoff = zeroBackoff{} })

			_, err := provider.clientsFor("", tt.policy).EKS.ListClusters(context.Background(), &eks.ListClustersInput{})

			if !IsDialFailure(err) {
				t.Fatalf("error = %v, want a dial failure", err)
			}
			if got := dials.Load(); got != tt.wantDials {
				t.Errorf("dials = %d, want %d", got, tt.wantDials)
			}
			if got := sweeps.Load() > 0; got != tt.wantSweep {
				t.Errorf("dial with the sweep bound = %t, want %t", got, tt.wantSweep)
			}
		})
	}
}

type zeroBackoff struct{}

func (zeroBackoff) BackoffDelay(int, error) (time.Duration, error) { return 0, nil }

func TestHTTPClientSizesIdleConnections(t *testing.T) {
	tests := []struct {
		maxConcurrency int
		wantPerHost    int
		wantTotal      int
	}{
		{maxConcurrency: 0, wantPerHost: 10, wantTotal: 100},
		{maxConcurrency: 8, wantPerHost: 10, wantTotal: 100},
		{maxConcurrency: 32, wantPerHost: 32, wantTotal: 128},
	}
	for _, tt := range tests {
		tr := NewHTTPClient(tt.maxConcurrency).GetTransport()
		if tr.MaxIdleConnsPerHost != tt.wantPerHost || tr.MaxIdleConns != tt.wantTotal {
			t.Errorf("max concurrency %d: idle connections = %d per host, %d in total; want %d, %d",
				tt.maxConcurrency, tr.MaxIdleConnsPerHost, tr.MaxIdleConns, tt.wantPerHost, tt.wantTotal)
		}
	}
}

func TestHTTPClientLimitsAStalledCall(t *testing.T) {
	tr := NewHTTPClient(8).GetTransport()
	if tr.ResponseHeaderTimeout != time.Minute {
		t.Errorf("ResponseHeaderTimeout = %v, want 1m", tr.ResponseHeaderTimeout)
	}
	if tr.HTTP2 == nil || tr.HTTP2.SendPingTimeout != 30*time.Second || tr.HTTP2.PingTimeout != 15*time.Second {
		t.Errorf("HTTP2 = %+v, want a ping after 30s of silence and 15s for its answer", tr.HTTP2)
	}
}

// With a defaults mode set, a service client gives a buildable HTTP client
// a dialer of its own, in a copy that no other client shares. Load freezes
// the client, so two services still share one connection and a sweep keeps
// its dial timeout.
func TestLoadSharesOneTransportUnderADefaultsMode(t *testing.T) {
	isolateAWS(t)
	srv := awsxtest.NewServer(fleettest.New())
	defer srv.Close()
	t.Setenv("AWS_ENDPOINT_URL", srv.URL)
	t.Setenv("AWS_DEFAULTS_MODE", "standard")

	var dials, sweepDials atomic.Int32
	client := NewHTTPClient(8).WithTransportOptions(func(tr *http.Transport) {
		next := tr.DialContext
		tr.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
			dials.Add(1)
			if sweep, _ := ctx.Value(sweepDialKey{}).(bool); sweep {
				sweepDials.Add(1)
			}
			return next(ctx, network, addr)
		}
	})

	cfg, err := load(context.Background(), Options{Region: "us-east-1"}, client)
	if err != nil {
		t.Fatalf("load() error = %v", err)
	}
	if cfg.DefaultsMode != aws.DefaultsModeStandard {
		t.Fatalf("defaults mode = %q, want standard: the test would prove nothing", cfg.DefaultsMode)
	}
	frozen, ok := cfg.HTTPClient.(*http.Client)
	if !ok {
		t.Fatalf("HTTP client is %T, want a frozen *http.Client", cfg.HTTPClient)
	}
	defer frozen.CloseIdleConnections()
	provider := NewProvider(cfg)

	sweep := provider.SweepClients("")
	if _, err := sweep.EKS.ListClusters(context.Background(), &eks.ListClustersInput{}); err != nil {
		t.Fatalf("eks:ListClusters: %v", err)
	}
	if _, err := sweep.STS.GetCallerIdentity(context.Background(), &sts.GetCallerIdentityInput{}); err != nil {
		t.Fatalf("sts:GetCallerIdentity: %v", err)
	}
	if _, err := provider.Clients("").EKS.ListClusters(context.Background(), &eks.ListClustersInput{}); err != nil {
		t.Fatalf("eks:ListClusters with the standard clients: %v", err)
	}

	if got := dials.Load(); got != 1 {
		t.Errorf("dials = %d, want 1: three calls of two services and two policies share a connection", got)
	}
	if got := sweepDials.Load(); got != 1 {
		t.Errorf("dials with the sweep bound = %d, want 1: the wrapper of the dialer was dropped", got)
	}
}

// The delay before a retry is random up to a ceiling that doubles from
// 200 ms and stops at 5 s.
func TestFullJitterBackoff(t *testing.T) {
	ceilings := []time.Duration{
		200 * time.Millisecond, 400 * time.Millisecond, 800 * time.Millisecond, 1600 * time.Millisecond,
		3200 * time.Millisecond, 5 * time.Second, 5 * time.Second,
	}
	for i, want := range ceilings {
		attempt := i + 1
		if got := backoffCeiling(attempt); got != want {
			t.Errorf("backoffCeiling(%d) = %v, want %v", attempt, got, want)
		}
		var longest time.Duration
		distinct := map[time.Duration]bool{}
		for range 2000 {
			d, err := fullJitterBackoff{}.BackoffDelay(attempt, nil)
			if err != nil || d < 0 || d > want {
				t.Fatalf("BackoffDelay(%d) = %v, %v; want a delay from 0 to %v", attempt, d, err, want)
			}
			longest = max(longest, d)
			distinct[d] = true
		}
		// Full jitter: the delays spread over the whole range.
		if longest < want/2 || len(distinct) < 1000 {
			t.Errorf("BackoffDelay(%d): longest of 2000 = %v with %d distinct values; want a spread up to %v", attempt, longest, len(distinct), want)
		}
	}
	if got := backoffCeiling(1000); got != 5*time.Second {
		t.Errorf("backoffCeiling(1000) = %v, want the 5s cap", got)
	}
	// Five attempts have four delays between them.
	var worst time.Duration
	for attempt := 1; attempt < maxAttempts; attempt++ {
		worst += backoffCeiling(attempt)
	}
	if worst != 3*time.Second {
		t.Errorf("longest total wait of %d attempts = %v, want 3s", maxAttempts, worst)
	}
}

// The standard retryer waits by this curve when no test replaces it.
func TestRetryerUsesFullJitterBackoff(t *testing.T) {
	for _, policy := range []Policy{Standard, Sweep} {
		retryer := newRetryer(policy, nil)
		throttled := &smithy.GenericAPIError{Code: "ThrottlingException"}
		for attempt := 1; attempt <= 6; attempt++ {
			for range 200 {
				d, err := retryer.RetryDelay(attempt, throttled)
				if err != nil || d > backoffCeiling(attempt) {
					t.Fatalf("%v: RetryDelay(%d) = %v, %v; want at most %v", policy, attempt, d, err, backoffCeiling(attempt))
				}
			}
		}
		if got := retryer.MaxAttempts(); got != 5 {
			t.Errorf("%v: MaxAttempts() = %d, want 5", policy, got)
		}
	}
	if got := newRetryer(Poll, nil).MaxAttempts(); got != 1 {
		t.Errorf("Poll: MaxAttempts() = %d, want 1", got)
	}
}

// A credential_process that stalls does not hold Load past its context: the
// credentials cache returns when the context ends. The process itself goes
// on until it ends; the SDK does not stop it.
func TestLoadIsBoundedWithAStalledCredentialProcess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the test uses the sleep program as the credential process")
	}
	isolateAWS(t)
	t.Setenv("AWS_ACCESS_KEY_ID", "")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "")
	// The process outlives the 20 ms of the context and ends soon after,
	// so that no goroutine of the SDK is left when the tests end.
	config := "[profile slow]\nregion = us-east-1\ncredential_process = sleep 0.3\n"
	if err := os.WriteFile(os.Getenv("AWS_CONFIG_FILE"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	_, err := Load(ctx, Options{Profile: "slow"})

	// An unbounded Load would return after the process ended, with the
	// error for its empty output.
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Load() error = %v, want the deadline of its context", err)
	}
}
