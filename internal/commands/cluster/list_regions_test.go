package cluster

import (
	"strings"
	"testing"

	"github.com/dantech2000/refresh/internal/commands/runner"
	"github.com/dantech2000/refresh/internal/mocks/fakeaws"
	"github.com/dantech2000/refresh/internal/ui/plaintest"
)

func listWorld() *fakeaws.Cluster {
	return &fakeaws.Cluster{Name: "prod", Version: "1.32", Nodegroups: []*fakeaws.Nodegroup{{Name: "web", Version: "1.32"}}}
}

// When the deadline ends while regions still wait for a slot, those regions
// count as failed. A sweep where no region answered is an error, never
// `{"clusters":[],"count":0}` with exit 0.
func TestListAllRegions_HangingEKSFails(t *testing.T) {
	srv := fakeaws.New(t, listWorld())
	t.Setenv("REFRESH_EKS_REGIONS", "us-east-1,us-west-2,eu-west-1")
	srv.HangEKS()

	stdout, stderr, err := runCluster(t, "list", "-A", "-C", "1", "-t", "300ms", "-o", "json")
	if err == nil {
		t.Fatalf("cluster list succeeded against a hanging EKS\nstdout:\n%s\nstderr:\n%s", stdout, stderr)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty on failure", stdout)
	}
	if !strings.Contains(err.Error(), "failed in all 3 regions") {
		t.Errorf("error = %v, want every region reported as failed", err)
	}
	if code := runner.ExitCodeOf(err); code != runner.ExitError {
		t.Errorf("exit code = %d, want 1 (total failure)", code)
	}
}

// A partial failure prints what was gathered, warns once per failed region,
// and exits 4 (REF-165).
func TestListAllRegions_PartialFailureWarns(t *testing.T) {
	srv := fakeaws.New(t, listWorld())
	t.Setenv("REFRESH_EKS_REGIONS", "us-east-1,eu-west-1")
	srv.FailRegions(func(region string) string {
		if region == "eu-west-1" {
			return "AccessDeniedException"
		}
		return ""
	})

	stdout, stderr, err := runCluster(t, "list", "-A", "-o", "json")
	if code := runner.ExitCodeOf(err); code != runner.ExitIncomplete {
		t.Fatalf("exit code = %d (err %v), want 4\nstderr:\n%s", code, err, stderr)
	}
	doc := fakeaws.RequireOneDocument(t, "json", stdout).(map[string]any)
	if doc["count"] != float64(1) {
		t.Errorf("count = %v, want 1 (us-east-1 only)", doc["count"])
	}
	// REFRESH_EKS_REGIONS scopes the sweep, so a denied region is a failure,
	// not a skip.
	if want := "warning: region eu-west-1: AccessDenied: AccessDeniedException"; strings.Count(stderr, want) != 1 {
		t.Errorf("stderr does not name eu-west-1 once (%q):\n%s", want, stderr)
	}
	if want := "incomplete data: 1 failure(s) (1 region)"; err.Error() != want {
		t.Errorf("err = %q, want %q", err, want)
	}
	if strings.Contains(stderr, "level=WARN") {
		t.Errorf("stderr still has a raw slog line:\n%s", stderr)
	}

	// The table and tree views print the gathered rows and list the failure
	// in their INCOMPLETE DATA section (not again on stderr), then exit 4 too.
	for _, args := range [][]string{{"list", "-A"}, {"list", "--tree"}} {
		stdout, stderr, err := runCluster(t, args...)
		if code := runner.ExitCodeOf(err); code != runner.ExitIncomplete {
			t.Errorf("%v: exit code = %d (err %v), want 4", args, code, err)
		}
		for _, want := range []string{"prod", "INCOMPLETE DATA", "region eu-west-1: AccessDenied"} {
			if !strings.Contains(stdout, want) {
				t.Errorf("%v: stdout lacks %q:\n%s", args, want, stdout)
			}
		}
		if strings.Contains(stderr, "warning: region") {
			t.Errorf("%v: the table run repeats the failure on stderr:\n%s", args, stderr)
		}
	}
}

// The default sweep (-A with no -r and no REFRESH_EKS_REGIONS) skips regions
// closed to these credentials with one note, exactly like status -A.
func TestListAllRegions_DefaultSweepSkipsInaccessible(t *testing.T) {
	srv := fakeaws.New(t, listWorld())
	srv.FailRegions(func(region string) string {
		if region == "us-east-1" {
			return ""
		}
		return "AccessDeniedException"
	})

	stdout, stderr, err := runCluster(t, "list", "-A", "-o", "json")
	if err != nil {
		t.Fatalf("cluster list: %v (skipped regions are not failures: want exit 0)\nstderr:\n%s", err, stderr)
	}
	doc := fakeaws.RequireOneDocument(t, "json", stdout).(map[string]any)
	if doc["count"] != float64(1) {
		t.Errorf("count = %v, want 1", doc["count"])
	}
	if !strings.Contains(stderr, "not accessible to these credentials") || strings.Contains(stderr, "warning: region") {
		t.Errorf("stderr should carry one skip note and no region warnings:\n%s", stderr)
	}

	// Every region closed: an error, not an empty list.
	srv.FailRegions(func(string) string { return "AccessDeniedException" })
	stdout, _, err = runCluster(t, "list", "-A", "-o", "json")
	if err == nil || !strings.Contains(err.Error(), "none is accessible") {
		t.Errorf("err = %v, want a none-accessible error", err)
	}
	if code := runner.ExitCodeOf(err); code != runner.ExitError {
		t.Errorf("exit code = %d, want 1 (total failure)", code)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty", stdout)
	}
}

// --tree renders the tree (it used to print the flat table because --format
// defaults to "table"); an explicit -o wins over --tree.
func TestListTreeFlag(t *testing.T) {
	fakeaws.New(t, listWorld())
	t.Setenv("REFRESH_EKS_REGIONS", "us-east-1")

	stdout, stderr, err := runCluster(t, "list", "--tree")
	if err != nil {
		t.Fatalf("cluster list --tree: %v\nstderr:\n%s", err, stderr)
	}
	if !strings.Contains(stdout, "REGION us-east-1") || !strings.Contains(stdout, "CLUSTER prod (ACTIVE") {
		t.Errorf("--tree did not render the region tree:\n%s", stdout)
	}
	if strings.Contains(stdout, "NAME") {
		t.Errorf("--tree printed the table header:\n%s", stdout)
	}
	// stdout is a pipe: the pterm tree must carry no escape codes.
	if strings.Contains(stdout, "\x1b") {
		t.Errorf("piped tree output has ANSI escapes: %q", stdout)
	}

	stdout, _, err = runCluster(t, "list", "--tree", "-o", "json")
	if err != nil {
		t.Fatal(err)
	}
	fakeaws.RequireOneDocument(t, "json", stdout)
}

func TestWantsTree(t *testing.T) {
	for _, tc := range []struct {
		format          string
		tree, formatSet bool
		want            bool
	}{
		{"table", false, false, false},
		{"table", true, false, true},
		{"tree", false, true, true},
		{"json", true, true, false},
		{"table", true, true, false},
	} {
		if got := wantsTree(tc.format, tc.tree, tc.formatSet); got != tc.want {
			t.Errorf("wantsTree(%q, tree=%v, set=%v) = %v, want %v", tc.format, tc.tree, tc.formatSet, got, tc.want)
		}
	}
}

// The global --region scans one region without -A, and with -A only sets the
// home region: the sweep still covers REFRESH_EKS_REGIONS.
func TestListGlobalRegionWithAndWithoutSweep(t *testing.T) {
	fakeaws.New(t, listWorld())
	t.Setenv("REFRESH_EKS_REGIONS", "us-west-2,ap-south-1")
	for _, tc := range []struct {
		args []string
		want float64
	}{
		{[]string{"refresh", "--region", "eu-west-1", "cluster", "list", "-o", "json"}, 1},
		{[]string{"refresh", "--region", "eu-west-1", "cluster", "list", "-A", "-o", "json"}, 2},
	} {
		stdout, stderr, err := fakeaws.Run(t, fakeaws.App(Command()), tc.args...)
		if err != nil {
			t.Fatalf("%v: %v\nstderr:\n%s", tc.args, err, stderr)
		}
		doc := fakeaws.RequireOneDocument(t, "json", stdout).(map[string]any)
		if doc["count"] != tc.want {
			t.Errorf("%v: count = %v, want %v (one row per scanned region)", tc.args, doc["count"], tc.want)
		}
	}
}

// A region that fails an explicit -r sweep is a Region failure
// (eks:ListClusters) in the JSON/YAML document, not only named on stderr.
// -o plain stays pure TSV: the failure is on stderr only.
func TestListRegions_FailuresInDocument(t *testing.T) {
	srv := fakeaws.New(t, listWorld())
	srv.FailRegions(func(region string) string {
		if region == "us-west-2" {
			return "ExpiredTokenException"
		}
		return ""
	})
	want := map[string]any{
		"kind":         "Region",
		"name":         "us-west-2",
		"region":       "us-west-2",
		"operation":    "eks:ListClusters",
		"reason":       "CredentialError",
		"retryable":    false,
		"awsErrorCode": "ExpiredTokenException",
		"error":        "ExpiredTokenException: fakeaws: region us-west-2 answers ExpiredTokenException",
	}

	for _, format := range []string{"json", "yaml"} {
		stdout, stderr, err := runCluster(t, "list", "-r", "us-east-1", "-r", "us-west-2", "-o", format)
		if code := runner.ExitCodeOf(err); code != runner.ExitIncomplete {
			t.Fatalf("-o %s: exit code = %d (err %v), want 4\nstderr:\n%s", format, code, err, stderr)
		}
		fakeaws.RequireFailures(t, fakeaws.RequireOneDocument(t, format, stdout), want)
		if !strings.Contains(stderr, "warning: region us-west-2: CredentialError: ExpiredTokenException") {
			t.Errorf("-o %s: stderr does not name the failed region:\n%s", format, stderr)
		}
	}

	stdout, stderr, err := runCluster(t, "list", "-r", "us-east-1", "-r", "us-west-2", "-o", "plain")
	if code := runner.ExitCodeOf(err); code != runner.ExitIncomplete {
		t.Fatalf("-o plain: exit code = %d (err %v), want 4", code, err)
	}
	plaintest.Check(t, stdout, "CLUSTER", "STATUS", "VERSION", "NODES")
	if strings.Contains(stdout, "us-west-2") || strings.Contains(stdout, "failures") {
		t.Errorf("-o plain stdout carries the failure; it belongs on stderr:\n%s", stdout)
	}
	if !strings.Contains(stderr, "warning: region us-west-2") {
		t.Errorf("-o plain: stderr does not name the failed region:\n%s", stderr)
	}

	// Every region answered: failures is [].
	srv.FailRegions(nil)
	stdout, _, err = runCluster(t, "list", "-r", "us-east-1", "-r", "us-west-2", "-o", "json")
	if err != nil {
		t.Fatalf("complete sweep: %v", err)
	}
	fakeaws.RequireFailures(t, fakeaws.RequireOneDocument(t, "json", stdout))
}
