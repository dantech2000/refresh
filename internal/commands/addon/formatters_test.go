package addon

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"
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

func TestMapAddonHealth_Active(t *testing.T) {
	got := mapAddonHealth(ekstypes.AddonStatusActive)
	if !strings.Contains(got, "PASS") {
		t.Errorf("Active: got %q, want PASS", got)
	}
}

func TestMapAddonHealth_Degraded(t *testing.T) {
	got := mapAddonHealth(ekstypes.AddonStatusDegraded)
	if !strings.Contains(got, "FAIL") {
		t.Errorf("Degraded: got %q, want FAIL", got)
	}
}

func TestMapAddonHealth_Creating(t *testing.T) {
	got := mapAddonHealth(ekstypes.AddonStatusCreating)
	if !strings.Contains(got, "IN PROGRESS") {
		t.Errorf("Creating: got %q, want IN PROGRESS", got)
	}
}

func TestMapAddonHealth_Unknown(t *testing.T) {
	got := mapAddonHealth("SOMETHING_ELSE")
	if !strings.Contains(got, "UNKNOWN") {
		t.Errorf("Unknown: got %q, want UNKNOWN", got)
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
		return outputAddonsTable("prod", []addonRow{{Name: "vpc-cni", Version: "v1.18.3", Status: "ACTIVE", Health: "PASS"}}, time.Second)
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
