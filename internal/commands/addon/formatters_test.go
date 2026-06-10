package addon

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/dantech2000/refresh/internal/services/addons"
)

func captureStdout(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	original := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	t.Cleanup(func() { os.Stdout = original })

	callErr := fn()
	_ = w.Close()
	var buf bytes.Buffer
	_, _ = io.Copy(&buf, r)
	return buf.String(), callErr
}

func TestHealthBadge(t *testing.T) {
	cases := map[string]string{
		"PASS":        "PASS",
		"FAIL":        "FAIL",
		"IN_PROGRESS": "IN PROGRESS",
		"SOMETHING":   "UNKNOWN",
	}
	for in, want := range cases {
		if got := healthBadge(in); !strings.Contains(got, want) {
			t.Errorf("healthBadge(%q) = %q, want it to contain %q", in, got, want)
		}
	}
	if got := healthBadge(""); got != "" {
		t.Errorf("healthBadge(\"\") = %q, want empty", got)
	}
}

func TestOutputAddonsTable_Empty(t *testing.T) {
	out, err := captureStdout(t, func() error { return outputAddonsTable("prod", nil, time.Second) })
	if err != nil {
		t.Fatalf("empty addons table: %v", err)
	}
	if !strings.Contains(out, "prod") {
		t.Errorf("empty table: missing cluster name 'prod' in output: %q", out)
	}
}

func TestOutputAddonsTable_WithRows(t *testing.T) {
	// pterm table rows (vpc-cni, version, status) are rendered to the original stdout
	// file handle and are not captured by captureStdout. We verify the header is correct
	// and that no error is returned. The JSON/YAML tests validate the data itself.
	out, err := captureStdout(t, func() error {
		return outputAddonsTable("prod", []addons.AddonSummary{{Name: "vpc-cni", Version: "v1.18.3", Status: "ACTIVE", Health: "PASS"}}, time.Second)
	})
	if err != nil {
		t.Fatalf("addons table: %v", err)
	}
	if !strings.Contains(out, "prod") {
		t.Errorf("addons table: missing cluster name 'prod' in header: %q", out)
	}
	if !strings.Contains(out, "Retrieved") {
		t.Errorf("addons table: missing 'Retrieved' timing line: %q", out)
	}
}
