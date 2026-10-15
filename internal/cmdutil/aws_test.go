package cmdutil_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/eks"

	"github.com/dantech2000/refresh/internal/awsx/awsxtest"
	"github.com/dantech2000/refresh/internal/cmdutil"
	"github.com/dantech2000/refresh/internal/cmdutil/cmdutiltest"
	"github.com/dantech2000/refresh/internal/contexts"
	"github.com/dantech2000/refresh/internal/fleet/fleettest"
)

// isolateAWS gives a test an AWS environment of its own: an empty config
// file, static keys, and no variable that names a profile or a region.
// Nothing reads the real ~/.aws.
func isolateAWS(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", dir)
	t.Setenv("AWS_CONFIG_FILE", path)
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", filepath.Join(dir, "none"))
	t.Setenv("AWS_ACCESS_KEY_ID", "AKIDFAKEFAKEFAKE")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "fake-secret")
	t.Setenv("AWS_SESSION_TOKEN", "")
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")
	for _, name := range []string{"AWS_PROFILE", "AWS_DEFAULT_PROFILE", "AWS_REGION", "AWS_DEFAULT_REGION", contexts.EnvContext} {
		t.Setenv(name, "")
	}
}

func TestNewAWS(t *testing.T) {
	prod := &contexts.File{
		Current:  "prod",
		Contexts: map[string]contexts.Context{"prod": {Cluster: "prod-eks", Region: "eu-west-1"}},
	}
	tests := []struct {
		name       string
		file       *contexts.File
		global     cmdutil.GlobalOptions
		env        map[string]string
		wantRegion string
		wantErr    string
	}{
		{name: "no context, no flags"},
		{name: "region of the active context", file: prod, wantRegion: "eu-west-1"},
		{
			name:       "region flag beats the context",
			file:       prod,
			global:     cmdutil.GlobalOptions{Regions: []string{"us-west-2"}},
			wantRegion: "us-west-2",
		},
		{
			name:       "first region flag is the home region",
			global:     cmdutil.GlobalOptions{Regions: []string{" ", "us-west-2", "eu-north-1"}},
			wantRegion: "us-west-2",
		},
		{
			name:    "environment that contradicts the context",
			file:    prod,
			env:     map[string]string{"AWS_REGION": "ap-south-1"},
			wantErr: `AWS_REGION=ap-south-1 conflicts with region "eu-west-1" of the active context "prod"`,
		},
		{
			// Also with both flags set: a typing error fails every
			// command in the same way.
			name:    "unknown REFRESH_CONTEXT",
			file:    prod,
			global:  cmdutil.GlobalOptions{Regions: []string{"us-west-2"}, Profile: "p"},
			env:     map[string]string{contexts.EnvContext: "prdo"},
			wantErr: `unknown context "prdo" from REFRESH_CONTEXT; known: prod`,
		},
		{
			name:    "profile that does not exist",
			global:  cmdutil.GlobalOptions{Profile: "nope"},
			wantErr: "AWS profile \"nope\" not found\nCause: --profile names it",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			isolateAWS(t)
			for name, value := range tt.env {
				t.Setenv(name, value)
			}
			f, _, _, _ := cmdutiltest.NewFactory()
			cmdutiltest.NewContextStore(t, f, tt.file)
			f.Global = tt.global
			f.AWS = cmdutil.NewAWS(f)

			provider, err := f.AWS(context.Background())

			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("AWS() error = %v, want one that contains %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("AWS() error = %v", err)
			}
			if got := provider.Region(); got != tt.wantRegion {
				t.Errorf("region = %q, want %q", got, tt.wantRegion)
			}
			again, err := f.AWS(context.Background())
			if err != nil || again != provider {
				t.Errorf("second AWS() = %p, %v; want the first provider %p", again, err, provider)
			}
		})
	}
}

// A context store that cannot be read fails the load. To ignore it would
// drop the saved region and profile and reach whatever the AWS defaults
// name.
func TestNewAWSUnreadableContextStore(t *testing.T) {
	isolateAWS(t)
	f, _, _, _ := cmdutiltest.NewFactory()
	store := cmdutiltest.NewContextStore(t, f, nil)
	if err := os.WriteFile(store.Path, []byte("current: [unclosed"), 0o600); err != nil {
		t.Fatal(err)
	}
	f.AWS = cmdutil.NewAWS(f)

	_, err := f.AWS(context.Background())

	if err == nil || !strings.Contains(err.Error(), "parsing "+store.Path) {
		t.Errorf("AWS() error = %v, want a parse error that names the file", err)
	}
}

// The load runs under --timeout although the context of the command has no
// deadline, and a failed load is tried again by the next call.
func TestNewAWSLoadIsBoundedByTimeout(t *testing.T) {
	isolateAWS(t)
	// A credential source that stalls: the container credentials endpoint,
	// served here by a handler that holds each request until the test
	// releases it, and then answers with credentials.
	arrived := make(chan struct{}, 8)
	release := make(chan struct{})
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		arrived <- struct{}{}
		<-release
		_, _ = w.Write([]byte(`{"AccessKeyId":"AKIDFAKEFAKEFAKE","SecretAccessKey":"fake-secret","Token":"t","Expiration":"2099-01-01T00:00:00Z"}`))
	}))
	defer source.Close()
	t.Setenv("AWS_ACCESS_KEY_ID", "")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "")
	t.Setenv("AWS_CONTAINER_CREDENTIALS_FULL_URI", source.URL)
	f, _, _, _ := cmdutiltest.NewFactory()
	cmdutiltest.NewContextStore(t, f, nil)
	// A timeout that has passed when the load starts: the test waits for no
	// timer. The context of the command has no deadline, so the deadline
	// error can only come from --timeout.
	f.Global = cmdutil.GlobalOptions{Regions: []string{"us-east-1"}, Timeout: time.Nanosecond}
	f.AWS = cmdutil.NewAWS(f)

	_, err := f.AWS(context.Background())

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("AWS() error = %v, want the --timeout deadline", err)
	}

	// The SDK goes on with the request that it started. Let it end, so
	// that it leaves nothing behind.
	<-arrived
	close(release)

	// The next call succeeds: the failure was not kept.
	f.Global.Timeout = time.Minute
	if _, err := f.AWS(context.Background()); err != nil {
		t.Errorf("AWS() after a failed load: %v", err)
	}
}

// A command test reaches the fake server through the factory.
func TestUseAWS(t *testing.T) {
	world := fleettest.New()
	world.Edit(func(s *fleettest.State) {
		s.Clusters = append(s.Clusters, &fleettest.Cluster{Name: "prod", Version: "1.32"})
	})
	srv := awsxtest.NewServer(world)
	defer srv.Close()
	f, _, _, _ := cmdutiltest.NewFactory()
	if _, err := f.AWS(context.Background()); err == nil {
		t.Error("a new test factory reached AWS; want an error until UseAWS")
	}
	cmdutiltest.UseAWS(f, srv.Config("us-east-1"))

	provider, err := f.AWS(context.Background())
	if err != nil {
		t.Fatalf("AWS() error = %v", err)
	}
	out, err := provider.Clients("").EKS.ListClusters(context.Background(), &eks.ListClustersInput{})
	if err != nil || len(out.Clusters) != 1 || out.Clusters[0] != "prod" {
		t.Errorf("ListClusters = %v, %v; want [prod]", out, err)
	}
}

// The profile in effect follows the rules of NewAWS: the flag, the
// context, then AWS_PROFILE and AWS_DEFAULT_PROFILE.
func TestProfileInEffect(t *testing.T) {
	withProfile := &contexts.File{
		Current:  "prod",
		Contexts: map[string]contexts.Context{"prod": {Cluster: "prod-eks", Profile: "ctx-profile"}},
	}
	tests := []struct {
		name string
		flag string
		file *contexts.File
		env  map[string]string
		want string
	}{
		{name: "nothing"},
		{name: "the flag", flag: "flag-profile", file: withProfile, want: "flag-profile"},
		{name: "the context", file: withProfile, want: "ctx-profile"},
		{name: "AWS_PROFILE", env: map[string]string{"AWS_PROFILE": "refresh-e2e"}, want: "refresh-e2e"},
		{name: "AWS_DEFAULT_PROFILE", env: map[string]string{"AWS_DEFAULT_PROFILE": "default-e2e"}, want: "default-e2e"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			isolateAWS(t)
			for k, v := range tt.env {
				t.Setenv(k, v)
			}
			f, _, _, _ := cmdutiltest.NewFactory()
			cmdutiltest.NewContextStore(t, f, tt.file)
			f.Global.Profile = tt.flag
			got, err := cmdutil.ProfileInEffect(f)
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Errorf("profile = %q, want %q", got, tt.want)
			}
		})
	}
}
