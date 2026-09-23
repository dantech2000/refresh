package statuscmd

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/urfave/cli/v3"

	"github.com/dantech2000/refresh/internal/commands/runner"
	"github.com/dantech2000/refresh/internal/mocks"
	statussvc "github.com/dantech2000/refresh/internal/services/status"
)

// fakeAWSEnv points the AWS SDK at static credentials and a local STS that
// answers GetCallerIdentity, so runner.SetupAWS succeeds with no network.
func fakeAWSEnv(t *testing.T) {
	t.Helper()
	sts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/xml")
		_, _ = io.WriteString(w, `<GetCallerIdentityResponse xmlns="https://sts.amazonaws.com/doc/2011-06-15/">
<GetCallerIdentityResult><Arn>arn:aws:iam::123456789012:user/test</Arn><UserId>AIDATEST</UserId><Account>123456789012</Account></GetCallerIdentityResult>
<ResponseMetadata><RequestId>req-1</RequestId></ResponseMetadata></GetCallerIdentityResponse>`)
	}))
	t.Cleanup(sts.Close)

	dir := t.TempDir()
	for k, v := range map[string]string{
		"AWS_ACCESS_KEY_ID":           "AKIATEST",
		"AWS_SECRET_ACCESS_KEY":       "secret",
		"AWS_SESSION_TOKEN":           "",
		"AWS_PROFILE":                 "",
		"AWS_REGION":                  "us-east-1",
		"AWS_DEFAULT_REGION":          "",
		"AWS_CONFIG_FILE":             filepath.Join(dir, "config"),
		"AWS_SHARED_CREDENTIALS_FILE": filepath.Join(dir, "credentials"),
		"AWS_EC2_METADATA_DISABLED":   "true",
		"AWS_ENDPOINT_URL_STS":        sts.URL,
		"REFRESH_CONFIG_HOME":         dir,
		"REFRESH_CONTEXT":             "",
		"REFRESH_EKS_REGIONS":         "",
	} {
		t.Setenv(k, v)
	}
}

// runStatusCLI runs `refresh status args...` under a root that carries the
// global flags status reads, and returns stdout and the action's error.
// Exit-coded errors are returned, not passed to os.Exit.
func runStatusCLI(t *testing.T, args ...string) (string, error) {
	t.Helper()
	return runRefreshCLI(t, append([]string{"status"}, args...)...)
}

// runRefreshCLI runs `refresh args...`, so a test can place global flags
// before the subcommand.
func runRefreshCLI(t *testing.T, args ...string) (string, error) {
	t.Helper()
	root := &cli.Command{
		Name: "refresh",
		Flags: []cli.Flag{
			&cli.DurationFlag{Name: "timeout", Value: time.Minute},
			&cli.IntFlag{Name: "max-concurrency", Value: 4},
			&cli.StringFlag{Name: "region"},
		},
		Commands:       []*cli.Command{Command()},
		ExitErrHandler: func(context.Context, *cli.Command, error) {},
	}

	orig := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	var runErr error
	func() {
		defer func() { os.Stdout = orig }()
		runErr = root.Run(context.Background(), append([]string{"refresh"}, args...))
	}()
	_ = w.Close()
	var buf bytes.Buffer
	_, _ = io.Copy(&buf, r)
	return buf.String(), runErr
}

func TestRunStatus_JSONAllCurrentExitsZero(t *testing.T) {
	fakeAWSEnv(t)
	stubRegionService(t, func(cfg aws.Config) regionLister {
		return fakeRegion{statuses: []statussvc.ClusterStatus{{
			Name: "prod-" + cfg.Region, Region: cfg.Region, Version: "1.33",
			Support: statussvc.SupportPosture{Tier: statussvc.SupportStandard},
		}}}
	})

	out, err := runStatusCLI(t, "-r", "us-east-1", "-r", "eu-west-1", "-o", "json", "--sort", "region", "--desc")
	if err != nil {
		t.Fatalf("status: %v (exit %d)", err, exitCode(err))
	}
	east, west := strings.Index(out, `"prod-us-east-1"`), strings.Index(out, `"prod-eu-west-1"`)
	if east < 0 || west < 0 {
		t.Fatalf("JSON should list both regions' clusters:\n%s", out)
	}
	if east > west {
		t.Errorf("--sort region --desc should list us-east-1 before eu-west-1:\n%s", out)
	}
}

// Only a sweep that returned no rows at all is fatal, and it carries the
// region's error.
func TestRunStatus_TotalFailureReturnsRegionError(t *testing.T) {
	fakeAWSEnv(t)
	stubRegionService(t, func(aws.Config) regionLister {
		return fakeRegion{err: mocks.AccessDenied()}
	})

	_, err := runStatusCLI(t, "-r", "us-east-1", "-r", "eu-west-1", "-o", "json")
	var re *regionError
	if !errors.As(err, &re) || re.Region != "us-east-1" {
		t.Fatalf("err = %v (%T), want the us-east-1 region error", err, err)
	}
	// Nothing was gathered: exit 1, not 4 (REF-165).
	if got := runner.ExitCodeOf(err); got != 1 {
		t.Errorf("exit = %d, want 1", got)
	}
}

// A region that answered with no clusters next to a failed one is partial
// data, not a total failure: exit 4, as in `cluster list` (REF-165).
func TestRunStatus_EmptyRegionPlusFailedRegionExitsIncomplete(t *testing.T) {
	fakeAWSEnv(t)
	stubRegionService(t, func(cfg aws.Config) regionLister {
		if cfg.Region == "eu-west-1" {
			return fakeRegion{err: mocks.AccessDenied()}
		}
		return fakeRegion{}
	})
	out, err := runStatusCLI(t, "-r", "us-east-1", "-r", "eu-west-1", "-o", "json")
	if got := exitCode(err); got != 4 {
		t.Fatalf("exit = %d (%v), want 4", got, err)
	}
	if !strings.Contains(out, `"clusters": []`) {
		t.Errorf("stdout should be the (empty) document:\n%s", out)
	}
}

// One failed region next to a healthy one is not fatal: the rows are
// printed and the run exits 4 (incomplete data), never 0.
func TestRunStatus_PartialFailureExitsIncomplete(t *testing.T) {
	fakeAWSEnv(t)
	stubRegionService(t, func(cfg aws.Config) regionLister {
		if cfg.Region == "eu-west-1" {
			return fakeRegion{err: mocks.AccessDenied()}
		}
		return fakeRegion{statuses: []statussvc.ClusterStatus{{
			Name: "prod", Region: cfg.Region, Version: "1.33",
			Support: statussvc.SupportPosture{Tier: statussvc.SupportStandard},
		}}}
	})

	out, err := runStatusCLI(t, "-r", "us-east-1", "-r", "eu-west-1", "-o", "plain")
	if got := exitCode(err); got != 4 {
		t.Fatalf("exit = %d (%v), want 4", got, err)
	}
	if !strings.Contains(out, "prod\tus-east-1\t1.33") {
		t.Errorf("plain output should still list the healthy region's row:\n%s", out)
	}
}

// An unknown --format fails before any AWS work.
func TestRunStatus_RejectsUnknownFormat(t *testing.T) {
	stubRegionService(t, func(aws.Config) regionLister {
		t.Error("no region may be swept for an invalid --format")
		return fakeRegion{}
	})
	if _, err := runStatusCLI(t, "-o", "xml"); err == nil {
		t.Fatal("status -o xml succeeded")
	}
}

// A fleet with no clusters prints `"clusters": []`, not null.
func TestRunStatus_EmptyFleetJSONIsEmptyList(t *testing.T) {
	fakeAWSEnv(t)
	stubRegionService(t, func(aws.Config) regionLister { return fakeRegion{} })

	out, err := runStatusCLI(t, "-r", "us-east-1", "-o", "json")
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if !strings.Contains(out, `"clusters": []`) {
		t.Errorf("empty fleet JSON = %s, want \"clusters\": []", out)
	}
}

// `refresh --region X status` sweeps X, like `refresh status --region X`.
// The local repeatable -r used to shadow the global flag, so the sweep ran in
// the default region instead.
func TestRunStatus_GlobalRegionBeforeSubcommand(t *testing.T) {
	fakeAWSEnv(t)
	for _, argv := range [][]string{
		{"--region", "eu-west-1", "status", "-o", "json"},
		{"status", "--region", "eu-west-1", "-o", "json"},
	} {
		var swept []string
		stubRegionService(t, func(cfg aws.Config) regionLister {
			swept = append(swept, cfg.Region)
			return fakeRegion{}
		})
		if _, err := runRefreshCLI(t, argv...); err != nil {
			t.Fatalf("%v: %v", argv, err)
		}
		if len(swept) != 1 || swept[0] != "eu-west-1" {
			t.Errorf("%v swept %v, want [eu-west-1]", argv, swept)
		}
	}
}

// With -A, the global --region is only the home region: the sweep still uses
// REFRESH_EKS_REGIONS (or the partition), not just that one region.
func TestRunStatus_GlobalRegionWithAllRegionsStillSweeps(t *testing.T) {
	fakeAWSEnv(t)
	t.Setenv("REFRESH_EKS_REGIONS", "us-west-2,ap-south-1")
	var mu sync.Mutex
	var swept []string
	stubRegionService(t, func(cfg aws.Config) regionLister {
		mu.Lock()
		swept = append(swept, cfg.Region)
		mu.Unlock()
		return fakeRegion{}
	})
	if _, err := runRefreshCLI(t, "--region", "us-east-1", "status", "-A", "-o", "json"); err != nil {
		t.Fatal(err)
	}
	sort.Strings(swept)
	if strings.Join(swept, ",") != "ap-south-1,us-west-2" {
		t.Errorf("swept %v, want the REFRESH_EKS_REGIONS set", swept)
	}
}
