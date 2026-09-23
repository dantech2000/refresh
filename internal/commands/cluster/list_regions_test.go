package cluster

import (
	"strings"
	"testing"

	"github.com/dantech2000/refresh/internal/mocks/fakeaws"
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
}

// A partial failure keeps exit 0 (REF-165 tracks a distinct code) but is
// visible: one stderr warning per failed region and an incomplete-list line.
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
	if err != nil {
		t.Fatalf("cluster list: %v\nstderr:\n%s", err, stderr)
	}
	doc := fakeaws.RequireOneDocument(t, "json", stdout).(map[string]any)
	if doc["count"] != float64(1) {
		t.Errorf("count = %v, want 1 (us-east-1 only)", doc["count"])
	}
	// REFRESH_EKS_REGIONS scopes the sweep, so a denied region is a failure,
	// not a skip.
	for _, want := range []string{"warning: region eu-west-1: AccessDeniedException", "1 of 2 region(s) failed"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr lacks %q:\n%s", want, stderr)
		}
	}
	if strings.Contains(stderr, "level=WARN") {
		t.Errorf("stderr still has a raw slog line:\n%s", stderr)
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
		t.Fatalf("cluster list: %v\nstderr:\n%s", err, stderr)
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
