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
		AMILatest:    "Latest",
		AMIOutdated:  "Outdated",
		AMIUpdating:  "Updating",
		AMIUnknown:   "Unknown",
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

func TestDryRunAction_PlainString(t *testing.T) {
	cases := map[DryRunAction]string{
		ActionUpdate:       "UPDATE",
		ActionSkipUpdating: "SKIP (already updating)",
		ActionSkipLatest:   "SKIP (already latest)",
		ActionForceUpdate:  "FORCE UPDATE",
		DryRunAction(99):   "UNKNOWN",
	}
	for a, want := range cases {
		if got := a.PlainString(); got != want {
			t.Errorf("DryRunAction(%d).PlainString() = %q, want %q", a, got, want)
		}
	}
}

func TestDryRunAction_Reason(t *testing.T) {
	cases := map[DryRunAction]string{
		ActionUpdate:       "AMI is outdated",
		ActionSkipUpdating: "Update already in progress",
		ActionSkipLatest:   "Already using latest AMI",
		ActionForceUpdate:  "Force flag specified",
		DryRunAction(99):   "Unknown reason",
	}
	for a, want := range cases {
		if got := a.Reason(); got != want {
			t.Errorf("DryRunAction(%d).Reason() = %q, want %q", a, got, want)
		}
	}
}

func TestDryRunAction_String(t *testing.T) {
	// Just verify all values produce non-empty output (color codes are present)
	actions := []DryRunAction{ActionUpdate, ActionSkipUpdating, ActionSkipLatest, ActionForceUpdate, DryRunAction(99)}
	for _, a := range actions {
		if s := a.String(); s == "" {
			t.Errorf("DryRunAction(%d).String() should not be empty", a)
		}
	}
}
