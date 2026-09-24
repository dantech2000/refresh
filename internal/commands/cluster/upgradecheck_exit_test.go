package cluster

import (
	"strings"
	"testing"

	"github.com/dantech2000/refresh/internal/commands/runner"
	"github.com/dantech2000/refresh/internal/mocks/fakeaws"
	"github.com/dantech2000/refresh/internal/ui/plaintest"
)

// `cluster upgrade-check` is a CI gate (REF-165): 0 ready, 2 warnings only,
// 3 blocked. The document is printed first, then the exit code applies.

func checkWorld(ngVersion string, addons []*fakeaws.Addon, insights ...*fakeaws.Insight) *fakeaws.Cluster {
	if insights == nil {
		insights = []*fakeaws.Insight{}
	}
	return &fakeaws.Cluster{
		Name:       "prod",
		Version:    "1.32",
		Nodegroups: []*fakeaws.Nodegroup{{Name: "web", Version: ngVersion}},
		Addons:     addons,
		Insights:   insights,
	}
}

func TestUpgradeCheckExitCodes(t *testing.T) {
	current := []*fakeaws.Addon{{Name: "vpc-cni", Version: "v1.19.0", Available: []string{"v1.19.0"}}}
	behind := []*fakeaws.Addon{{Name: "vpc-cni", Version: "v1.18.0", Available: []string{"v1.19.0", "v1.18.0"}}}
	insight := func(status string) *fakeaws.Insight {
		return &fakeaws.Insight{ID: "ins-" + strings.ToLower(status), Name: "Deprecated APIs " + status, Status: status}
	}

	for _, tc := range []struct {
		name    string
		world   *fakeaws.Cluster
		want    int
		wantMsg string
	}{
		{"ready", checkWorld("1.32", current, insight("PASSING")), runner.ExitOK, ""},
		{"warning insight", checkWorld("1.32", current, insight("WARNING")), runner.ExitNeedsAttention, "1 WARNING insight"},
		{"nodegroup behind", checkWorld("1.31", current), runner.ExitNeedsAttention, "1 nodegroup(s) behind"},
		{"addon behind", checkWorld("1.32", behind), runner.ExitNeedsAttention, "1 addon(s) behind latest"},
		{"error insight", checkWorld("1.32", current, insight("ERROR"), insight("WARNING")), runner.ExitBlocked, "1 ERROR insight"},
		{"unknown insight", checkWorld("1.32", current, insight("UNKNOWN")), runner.ExitBlocked, "1 UNKNOWN insight"},
		{"blocking skew", checkWorld("1.29", current), runner.ExitBlocked, "kubelet skew limit"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fakeaws.New(t, tc.world)
			stdout, stderr, err := runCluster(t, "upgrade-check", "prod", "-o", "json")
			if code := runner.ExitCodeOf(err); code != tc.want {
				t.Fatalf("exit code = %d (err %v), want %d\nstderr:\n%s", code, err, tc.want, stderr)
			}
			if tc.wantMsg != "" && !strings.Contains(err.Error(), tc.wantMsg) {
				t.Errorf("err = %v, want it to name %q", err, tc.wantMsg)
			}
			// The document comes first, whatever the verdict.
			doc := fakeaws.RequireOneDocument(t, "json", stdout).(map[string]any)
			if doc["cluster"] != "prod" {
				t.Errorf("cluster = %v, want prod", doc["cluster"])
			}

			// --exit-zero: report mode, same document, exit 0.
			stdout, stderr, err = runCluster(t, "upgrade-check", "prod", "-o", "json", "--exit-zero")
			if err != nil {
				t.Fatalf("--exit-zero: %v\nstderr:\n%s", err, stderr)
			}
			fakeaws.RequireOneDocument(t, "json", stdout)
		})
	}
}

// The table view gets the same exit code as the machine formats.
func TestUpgradeCheckExitCode_Table(t *testing.T) {
	fakeaws.New(t, checkWorld("1.32", nil, &fakeaws.Insight{ID: "ins-err", Name: "Deprecated APIs", Status: "ERROR"}))
	stdout, _, err := runCluster(t, "upgrade-check", "prod")
	if code := runner.ExitCodeOf(err); code != runner.ExitBlocked {
		t.Fatalf("exit code = %d (err %v), want 3", code, err)
	}
	if !strings.Contains(stdout, "Deprecated APIs") {
		t.Errorf("table does not show the insight:\n%s", stdout)
	}
}

// --status narrows the insights the gate looks at.
func TestUpgradeCheckExitCode_StatusFilter(t *testing.T) {
	fakeaws.New(t, checkWorld("1.32", nil,
		&fakeaws.Insight{ID: "ins-err", Name: "Deprecated APIs", Status: "ERROR"},
		&fakeaws.Insight{ID: "ins-warn", Name: "Kube-proxy skew", Status: "WARNING"},
	))
	_, _, err := runCluster(t, "upgrade-check", "prod", "--status", "WARNING", "-o", "json")
	if code := runner.ExitCodeOf(err); code != runner.ExitNeedsAttention {
		t.Fatalf("exit code = %d (err %v), want 2", code, err)
	}
}

// --id: the exit code reflects that one insight's status.
func TestUpgradeCheckExitCode_ID(t *testing.T) {
	world := checkWorld("1.29", nil,
		&fakeaws.Insight{ID: "ins-pass", Name: "Cluster health", Status: "PASSING"},
		&fakeaws.Insight{ID: "ins-warn", Name: "Kube-proxy skew", Status: "WARNING"},
		&fakeaws.Insight{ID: "ins-err", Name: "Deprecated APIs", Status: "ERROR"},
		&fakeaws.Insight{ID: "ins-unk", Name: "Addon compatibility", Status: "UNKNOWN"},
	)
	for _, tc := range []struct {
		id   string
		want int
	}{
		// The cluster has blocking skew, but --id looks at one insight only.
		{"ins-pass", runner.ExitOK},
		{"ins-warn", runner.ExitNeedsAttention},
		{"ins-err", runner.ExitBlocked},
		{"ins-unk", runner.ExitBlocked},
	} {
		t.Run(tc.id, func(t *testing.T) {
			fakeaws.New(t, world)
			stdout, stderr, err := runCluster(t, "upgrade-check", "prod", "--id", tc.id, "-o", "json")
			if code := runner.ExitCodeOf(err); code != tc.want {
				t.Fatalf("exit code = %d (err %v), want %d\nstderr:\n%s", code, err, tc.want, stderr)
			}
			doc := fakeaws.RequireOneDocument(t, "json", stdout).(map[string]any)
			if doc["id"] != tc.id {
				t.Errorf("id = %v, want %s", doc["id"], tc.id)
			}
			if _, _, err := runCluster(t, "upgrade-check", "prod", "--id", tc.id, "-o", "json", "--exit-zero"); err != nil {
				t.Errorf("--exit-zero: %v", err)
			}
		})
	}
}

// An AWS error is still exit 1, even with --exit-zero.
func TestUpgradeCheckExitCode_ErrorIsOne(t *testing.T) {
	fakeaws.New(t, checkWorld("1.32", nil))
	_, _, err := runCluster(t, "upgrade-check", "missing", "--exit-zero", "-o", "json")
	if code := runner.ExitCodeOf(err); code != runner.ExitError {
		t.Fatalf("exit code = %d (err %v), want 1", code, err)
	}
}

// A nodegroup or add-on whose skew could not be read makes the check
// incomplete (exit 4): the unread nodegroup could be the one past the skew
// limit. A blocker still wins (3); 4 wins over warnings (2).
func TestUpgradeCheckExitCode_Incomplete(t *testing.T) {
	denied := func(version string) *fakeaws.Cluster {
		w := checkWorld("1.32", nil)
		w.Nodegroups = append(w.Nodegroups, &fakeaws.Nodegroup{Name: "old", Version: version, DescribeNodegroupError: "AccessDeniedException"})
		return w
	}
	t.Run("unreadable nodegroup", func(t *testing.T) {
		fakeaws.New(t, denied("1.29"))
		stdout, stderr, err := runCluster(t, "upgrade-check", "prod", "-o", "json")
		if code := runner.ExitCodeOf(err); code != runner.ExitIncomplete {
			t.Fatalf("exit code = %d (err %v), want 4\nstderr:\n%s", code, err, stderr)
		}
		doc := fakeaws.RequireOneDocument(t, "json", stdout)
		fakeaws.RequireFailures(t, doc, map[string]any{
			"kind":         "Nodegroup",
			"name":         "old",
			"cluster":      "prod",
			"region":       "us-east-1",
			"operation":    "eks:DescribeNodegroup",
			"reason":       "AccessDenied",
			"retryable":    false,
			"awsErrorCode": "AccessDeniedException",
		})
		if _, ok := doc.(map[string]any)["incomplete"]; ok {
			t.Errorf("document still has the removed incomplete key")
		}
		if strings.Count(stderr, "warning: nodegroup prod/old (us-east-1): AccessDenied: AccessDeniedException") != 1 {
			t.Errorf("stderr does not name the unreadable nodegroup once:\n%s", stderr)
		}
		if want := "incomplete data: 1 failure(s) (1 nodegroup)"; err.Error() != want {
			t.Errorf("err = %q, want %q", err, want)
		}
		if _, _, err := runCluster(t, "upgrade-check", "prod", "-o", "json", "--exit-zero"); err != nil {
			t.Errorf("--exit-zero: %v", err)
		}
	})
	t.Run("table shows it", func(t *testing.T) {
		fakeaws.New(t, denied("1.29"))
		stdout, stderr, err := runCluster(t, "upgrade-check", "prod")
		if code := runner.ExitCodeOf(err); code != runner.ExitIncomplete {
			t.Fatalf("exit code = %d, want 4", code)
		}
		for _, want := range []string{"○ INCOMPLETE", "INCOMPLETE DATA", "nodegroup prod/old (us-east-1): AccessDenied"} {
			if !strings.Contains(stdout, want) {
				t.Errorf("table lacks %q:\n%s", want, stdout)
			}
		}
		if strings.Contains(stderr, "warning: nodegroup") {
			t.Errorf("the table run repeats the failure on stderr:\n%s", stderr)
		}
	})
	t.Run("plain names it on stderr", func(t *testing.T) {
		fakeaws.New(t, denied("1.29"))
		stdout, stderr, err := runCluster(t, "upgrade-check", "prod", "-o", "plain")
		if code := runner.ExitCodeOf(err); code != runner.ExitIncomplete {
			t.Fatalf("exit code = %d, want 4", code)
		}
		plaintest.Check(t, stdout, "ID", "NAME", "CATEGORY", "STATUS", "K8S", "LAST REFRESH")
		if !strings.Contains(stderr, "warning: nodegroup prod/old (us-east-1): AccessDenied") {
			t.Errorf("stderr lacks the unreadable nodegroup:\n%s", stderr)
		}
	})
	t.Run("blocker wins", func(t *testing.T) {
		w := denied("1.29")
		w.Insights = []*fakeaws.Insight{{ID: "ins-err", Name: "Deprecated APIs", Status: "ERROR"}}
		fakeaws.New(t, w)
		_, _, err := runCluster(t, "upgrade-check", "prod", "-o", "json")
		if code := runner.ExitCodeOf(err); code != runner.ExitBlocked {
			t.Fatalf("exit code = %d, want 3", code)
		}
	})
	t.Run("wins over warnings", func(t *testing.T) {
		w := denied("1.29")
		w.Insights = []*fakeaws.Insight{{ID: "ins-warn", Name: "Kube-proxy skew", Status: "WARNING"}}
		fakeaws.New(t, w)
		_, _, err := runCluster(t, "upgrade-check", "prod", "-o", "json")
		if code := runner.ExitCodeOf(err); code != runner.ExitIncomplete {
			t.Fatalf("exit code = %d, want 4", code)
		}
	})
	t.Run("unreadable addon", func(t *testing.T) {
		fakeaws.New(t, checkWorld("1.32", []*fakeaws.Addon{{Name: "vpc-cni", Version: "v1.19.0", DescribeAddonError: "AccessDeniedException"}}))
		_, _, err := runCluster(t, "upgrade-check", "prod", "-o", "json")
		if code := runner.ExitCodeOf(err); code != runner.ExitIncomplete {
			t.Fatalf("exit code = %d, want 4", code)
		}
	})
}
