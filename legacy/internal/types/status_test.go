package types

import (
	"strings"
	"testing"
)

func TestAMIStatus_String(t *testing.T) {
	cases := []struct {
		status AMIStatus
		want   string // substring of color-stripped output
	}{
		{AMILatest, "Latest"},
		{AMIOutdated, "Outdated"},
		{AMIUpdating, "Updating"},
		{AMIUnknown, "Unknown"},
		{AMIStatus(99), "Unknown"},
	}
	for _, tc := range cases {
		got := tc.status.String()
		if !strings.Contains(got, tc.want) {
			t.Errorf("AMIStatus(%d).String() = %q, want substring %q", tc.status, got, tc.want)
		}
	}
}

func TestAMIStatus_PlainString(t *testing.T) {
	cases := map[AMIStatus]string{
		AMILatest:     "Latest",
		AMIOutdated:   "Outdated",
		AMIUpdating:   "Updating",
		AMIUnknown:    "Unknown",
		AMIStatus(99): "Unknown",
	}
	for s, want := range cases {
		if got := s.PlainString(); got != want {
			t.Errorf("AMIStatus(%d).PlainString() = %q, want %q", s, got, want)
		}
	}
}

func TestAMIStatus_NeedsUpdate(t *testing.T) {
	if !AMIOutdated.NeedsUpdate() {
		t.Error("AMIOutdated.NeedsUpdate() should be true")
	}
	for _, s := range []AMIStatus{AMILatest, AMIUpdating, AMIUnknown} {
		if s.NeedsUpdate() {
			t.Errorf("AMIStatus(%d).NeedsUpdate() should be false", s)
		}
	}
}
