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

	"github.com/dantech2000/refresh/internal/awsx"
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
			wantErr: "failed to load AWS config: AWS profile \"nope\" not found\nCause: --profile names it",
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
	// A credential source that never answers: the container credentials
	// endpoint, served here by a handler that holds every request.
	release := make(chan struct{})
	stalled := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer stalled.Close()
	defer close(release)
	t.Setenv("AWS_ACCESS_KEY_ID", "")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "")
	t.Setenv("AWS_CONTAINER_CREDENTIALS_FULL_URI", stalled.URL)
	f, _, _, _ := cmdutiltest.NewFactory()
	cmdutiltest.NewContextStore(t, f, nil)
	f.Global = cmdutil.GlobalOptions{Regions: []string{"us-east-1"}, Timeout: 50 * time.Millisecond}
	f.AWS = cmdutil.NewAWS(f)

	start := time.Now()
	_, err := f.AWS(context.Background())

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("AWS() error = %v, want the --timeout deadline", err)
	}
	if d := time.Since(start); d > 30*time.Second {
		t.Errorf("AWS() took %v, want about the 50ms timeout", d)
	}

	// Without the stalled source the next call succeeds: the failure was
	// not kept.
	t.Setenv("AWS_CONTAINER_CREDENTIALS_FULL_URI", "")
	t.Setenv("AWS_ACCESS_KEY_ID", "AKIDFAKEFAKEFAKE")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "fake-secret")
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
	out, err := provider.Clients("", awsx.Read).EKS.ListClusters(context.Background(), &eks.ListClustersInput{})
	if err != nil || len(out.Clusters) != 1 || out.Clusters[0] != "prod" {
		t.Errorf("ListClusters = %v, %v; want [prod]", out, err)
	}
}
