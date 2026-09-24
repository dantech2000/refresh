package cluster

import (
	"strings"
	"testing"

	"github.com/dantech2000/refresh/internal/commands/runner"
	"github.com/dantech2000/refresh/internal/mocks/fakeaws"
	"github.com/dantech2000/refresh/internal/ui"
	"github.com/dantech2000/refresh/internal/ui/plaintest"
)

// cluster list: a nodegroup that could not be described leaves its cluster
// row incomplete (the node count is partial). The failure is reported once
// in each of three places: the document's failures, one stderr line, and
// exit 4.
func TestList_FailuresContract(t *testing.T) {
	world := func() *fakeaws.Cluster {
		return &fakeaws.Cluster{Name: "prod", Version: "1.32", Nodegroups: []*fakeaws.Nodegroup{
			{Name: "api", Version: "1.32"},
			{Name: "web", Version: "1.32", DescribeNodegroupError: "AccessDeniedException"},
		}}
	}
	web := map[string]any{
		"kind":         "Nodegroup",
		"name":         "web",
		"cluster":      "prod",
		"region":       "us-east-1",
		"operation":    "eks:DescribeNodegroup",
		"reason":       "AccessDenied",
		"retryable":    false,
		"awsErrorCode": "AccessDeniedException",
	}
	const warning = "warning: nodegroup prod/web (us-east-1): AccessDenied: AccessDeniedException: "

	for _, format := range []string{"json", "yaml"} {
		t.Run(format, func(t *testing.T) {
			fakeaws.New(t, world())
			stdout, stderr, err := runCluster(t, "list", "-o", format)
			if code := runner.ExitCodeOf(err); code != runner.ExitIncomplete {
				t.Fatalf("exit = %d (%v), want 4\nstderr:\n%s", code, err, stderr)
			}
			doc := fakeaws.RequireOneDocument(t, format, stdout)
			fakeaws.RequireFailures(t, doc, web)
			row := doc.(map[string]any)["clusters"].([]any)[0].(map[string]any)
			if row["incomplete"] != true {
				t.Errorf("row incomplete = %v, want true", row["incomplete"])
			}
			if _, ok := row["warnings"]; ok {
				t.Errorf("row still has the removed warnings key: %v", row)
			}
			if strings.Count(stderr, warning) != 1 {
				t.Errorf("stderr does not name web once:\n%s", stderr)
			}
		})
	}
	t.Run("plain", func(t *testing.T) {
		fakeaws.New(t, world())
		stdout, stderr, err := runCluster(t, "list", "-o", "plain")
		if code := runner.ExitCodeOf(err); code != runner.ExitIncomplete {
			t.Fatalf("exit = %d (%v), want 4", code, err)
		}
		// The partial count (api's 2 nodes) is still printed.
		rows := plaintest.Check(t, stdout, "CLUSTER", "STATUS", "VERSION", "NODES")
		if len(rows) != 1 || rows[0][3] != "2" {
			t.Errorf("rows = %q, want prod with the partial count 2", rows)
		}
		if !strings.Contains(stderr, warning) {
			t.Errorf("stderr lacks %q:\n%s", warning, stderr)
		}
	})
	t.Run("table marks the row and lists the failure", func(t *testing.T) {
		fakeaws.New(t, world())
		stdout, stderr, _ := runCluster(t, "list")
		stdout = ui.StripANSI(stdout)
		for _, want := range []string{"INCOMPLETE DATA", "nodegroup prod/web (us-east-1): AccessDenied"} {
			if !strings.Contains(stdout, want) {
				t.Errorf("table lacks %q:\n%s", want, stdout)
			}
		}
		if strings.Contains(stderr, "warning: nodegroup") {
			t.Errorf("table run repeats the failure on stderr:\n%s", stderr)
		}
	})
}

// cluster describe: an add-on that could not be described is left out of
// "addons" and reported in the document's failures (never as warnings
// text), on one stderr line, and by exit 4.
func TestDescribe_FailuresContract(t *testing.T) {
	world := func() *fakeaws.Cluster {
		return &fakeaws.Cluster{Name: "prod", Version: "1.32", Addons: []*fakeaws.Addon{
			{Name: "coredns", Version: "v1.11.4"},
			{Name: "vpc-cni", Version: "v1.19.0", DescribeAddonError: "AccessDeniedException"},
		}}
	}
	vpcCNI := map[string]any{
		"kind":         "Addon",
		"name":         "vpc-cni",
		"cluster":      "prod",
		"region":       "us-east-1",
		"operation":    "eks:DescribeAddon",
		"reason":       "AccessDenied",
		"retryable":    false,
		"awsErrorCode": "AccessDeniedException",
	}
	const warning = "warning: addon prod/vpc-cni (us-east-1): AccessDenied: AccessDeniedException: "

	for _, format := range []string{"json", "yaml"} {
		t.Run(format, func(t *testing.T) {
			fakeaws.New(t, world())
			stdout, stderr, err := runCluster(t, "describe", "prod", "--no-health", "-o", format)
			if code := runner.ExitCodeOf(err); code != runner.ExitIncomplete {
				t.Fatalf("exit = %d (%v), want 4\nstderr:\n%s", code, err, stderr)
			}
			doc := fakeaws.RequireOneDocument(t, format, stdout)
			fakeaws.RequireFailures(t, doc, vpcCNI)
			if _, ok := doc.(map[string]any)["warnings"]; ok {
				t.Errorf("document still has the removed warnings key")
			}
			if strings.Count(stderr, warning) != 1 {
				t.Errorf("stderr does not name vpc-cni once:\n%s", stderr)
			}
		})
	}
	t.Run("plain", func(t *testing.T) {
		fakeaws.New(t, world())
		stdout, stderr, err := runCluster(t, "describe", "prod", "--no-health", "-o", "plain")
		if code := runner.ExitCodeOf(err); code != runner.ExitIncomplete {
			t.Fatalf("exit = %d (%v), want 4", code, err)
		}
		plaintest.Check(t, stdout, ui.PlainKVHeaders...)
		if !strings.Contains(stderr, warning) {
			t.Errorf("stderr lacks %q:\n%s", warning, stderr)
		}
	})
	t.Run("table lists the failure", func(t *testing.T) {
		fakeaws.New(t, world())
		stdout, stderr, _ := runCluster(t, "describe", "prod", "--no-health")
		if !strings.Contains(stdout, "INCOMPLETE DATA") || !strings.Contains(stdout, "addon prod/vpc-cni (us-east-1): AccessDenied") {
			t.Errorf("table lacks the INCOMPLETE DATA section:\n%s", stdout)
		}
		if strings.Contains(stderr, "warning: addon") {
			t.Errorf("table run repeats the failure on stderr:\n%s", stderr)
		}
	})
	t.Run("complete describe has empty failures", func(t *testing.T) {
		fakeaws.New(t, &fakeaws.Cluster{Name: "prod", Version: "1.32", Addons: []*fakeaws.Addon{{Name: "coredns", Version: "v1.11.4"}}})
		stdout, _, err := runCluster(t, "describe", "prod", "--no-health", "-o", "json")
		if err != nil {
			t.Fatalf("describe: %v", err)
		}
		fakeaws.RequireFailures(t, fakeaws.RequireOneDocument(t, "json", stdout))
	})
}
