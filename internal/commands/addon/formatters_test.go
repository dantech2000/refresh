package addon

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/dantech2000/refresh/internal/services/addons"
	"github.com/dantech2000/refresh/internal/ui"
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
	rows := []addons.AddonSummary{{Name: "vpc-cni", Version: "v1.18.3", Status: "ACTIVE", Health: "PASS"}}

	// Human path (render design system): ADD-ONS header + tokenized rows.
	out, err := captureStdout(t, func() error { return outputAddonsTable("prod", rows, time.Second) })
	if err != nil {
		t.Fatalf("addons table: %v", err)
	}
	for _, want := range []string{"ADD-ONS", "prod", "vpc-cni", "ACTIVE"} {
		if !strings.Contains(out, want) {
			t.Errorf("addons table missing %q: %q", want, out)
		}
	}

	// Plain path (-o plain) keeps the cluster banner + "Retrieved" timing line.
	ui.SetPlainOutput(true)
	defer ui.SetPlainOutput(false)
	plain, err := captureStdout(t, func() error { return outputAddonsTable("prod", rows, time.Second) })
	if err != nil {
		t.Fatalf("addons plain: %v", err)
	}
	if !strings.Contains(plain, "Retrieved") {
		t.Errorf("addons plain: missing 'Retrieved' timing line: %q", plain)
	}
}
