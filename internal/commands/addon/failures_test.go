package addon

import (
	"strings"
	"testing"

	"github.com/dantech2000/refresh/internal/mocks/fakeaws"
	"github.com/dantech2000/refresh/internal/ui/plaintest"
)

// addon list: an add-on that could not be described is left out of the
// list and reported once, in three places: the document's failures, one
// stderr line, and exit 4.
func TestList_FailuresContract(t *testing.T) {
	world := func() *fakeaws.Cluster {
		return addonCluster(
			&fakeaws.Addon{Name: "coredns", Version: "v1.11.4"},
			&fakeaws.Addon{Name: "kube-proxy", Version: "v1.31.0", DescribeAddonError: "AccessDeniedException"},
		)
	}
	kubeProxy := map[string]any{
		"kind":         "Addon",
		"name":         "kube-proxy",
		"cluster":      "prod",
		"region":       "us-east-1",
		"operation":    "eks:DescribeAddon",
		"reason":       "AccessDenied",
		"retryable":    false,
		"awsErrorCode": "AccessDeniedException",
	}
	const warning = "warning: addon prod/kube-proxy (us-east-1): AccessDenied: AccessDeniedException: "

	for _, format := range []string{"json", "yaml"} {
		t.Run(format, func(t *testing.T) {
			fakeaws.New(t, world())
			stdout, stderr, err := runAddon(t, "list", "prod", "-o", format)
			if code := exitCodeOf(err); code != 4 {
				t.Fatalf("exit = %d (%v), want 4\nstderr:\n%s", code, err, stderr)
			}
			if want := "incomplete data: 1 failure(s) (1 addon)"; err.Error() != want {
				t.Errorf("err = %q, want %q", err, want)
			}
			doc := fakeaws.RequireOneDocument(t, format, stdout)
			fakeaws.RequireFailures(t, doc, kubeProxy)
			if n := doc.(map[string]any)["count"]; n != float64(1) && n != 1 {
				t.Errorf("count = %v, want 1", n)
			}
			if strings.Count(stderr, warning) != 1 {
				t.Errorf("stderr does not name kube-proxy once:\n%s", stderr)
			}
		})
	}
	t.Run("plain", func(t *testing.T) {
		fakeaws.New(t, world())
		stdout, stderr, err := runAddon(t, "list", "prod", "-o", "plain")
		if code := exitCodeOf(err); code != 4 {
			t.Fatalf("exit = %d (%v), want 4", code, err)
		}
		rows := plaintest.Check(t, stdout, "NAME", "VERSION", "STATUS", "HEALTH")
		if len(rows) != 1 || rows[0][0] != "coredns" {
			t.Errorf("rows = %q, want the coredns row only", rows)
		}
		if !strings.Contains(stderr, warning) {
			t.Errorf("stderr lacks %q:\n%s", warning, stderr)
		}
	})
	t.Run("table lists the failure", func(t *testing.T) {
		fakeaws.New(t, world())
		stdout, stderr, _ := runAddon(t, "list", "prod")
		if !strings.Contains(stdout, "INCOMPLETE DATA") || !strings.Contains(stdout, "addon prod/kube-proxy (us-east-1): AccessDenied") {
			t.Errorf("table lacks the INCOMPLETE DATA section:\n%s", stdout)
		}
		if strings.Contains(stderr, "warning: addon") {
			t.Errorf("table run repeats the failure on stderr:\n%s", stderr)
		}
	})
	t.Run("complete list has empty failures", func(t *testing.T) {
		fakeaws.New(t, addonCluster(&fakeaws.Addon{Name: "coredns", Version: "v1.11.4"}))
		stdout, _, err := runAddon(t, "list", "prod", "-o", "json")
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		fakeaws.RequireFailures(t, fakeaws.RequireOneDocument(t, "json", stdout))
	})
}

// addon describe reads one add-on or fails (exit 1), so its document always
// carries an empty failures list.
func TestDescribe_FailuresIsEmptyList(t *testing.T) {
	fakeaws.New(t, addonCluster(&fakeaws.Addon{Name: "coredns", Version: "v1.11.4"}))
	for _, format := range []string{"json", "yaml"} {
		stdout, stderr, err := runAddon(t, "describe", "prod", "coredns", "-o", format)
		if err != nil {
			t.Fatalf("-o %s: describe: %v\nstderr:\n%s", format, err, stderr)
		}
		fakeaws.RequireFailures(t, fakeaws.RequireOneDocument(t, format, stdout))
	}

	// A describe that cannot read the add-on is an error, not a failure.
	fakeaws.New(t, addonCluster(&fakeaws.Addon{Name: "coredns", Version: "v1.11.4", DescribeAddonError: "AccessDeniedException"}))
	stdout, _, err := runAddon(t, "describe", "prod", "coredns", "-o", "json")
	if code := exitCodeOf(err); code != 1 || stdout != "" {
		t.Errorf("exit = %d, stdout = %q; want 1 and no document", code, stdout)
	}
}
