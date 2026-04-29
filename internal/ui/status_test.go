package ui

import (
	"testing"

	ekstypes "github.com/aws/aws-sdk-go-v2/service/eks/types"
)

func TestGetStatusPrefix(t *testing.T) {
	cases := []struct {
		status ekstypes.UpdateStatus
		want   string
	}{
		{ekstypes.UpdateStatusInProgress, "[IN PROGRESS]"},
		{ekstypes.UpdateStatusSuccessful, "[SUCCESSFUL]"},
		{ekstypes.UpdateStatusFailed, "[FAILED]"},
		{ekstypes.UpdateStatusCancelled, "[CANCELLED]"},
		{"SOME_UNKNOWN", "[UNKNOWN]"},
	}
	for _, c := range cases {
		if got := GetStatusPrefix(c.status); got != c.want {
			t.Errorf("GetStatusPrefix(%q) = %q, want %q", c.status, got, c.want)
		}
	}
}

func TestGetStatusColor_ReturnsNonNilFunction(t *testing.T) {
	statuses := []ekstypes.UpdateStatus{
		ekstypes.UpdateStatusInProgress,
		ekstypes.UpdateStatusSuccessful,
		ekstypes.UpdateStatusFailed,
		ekstypes.UpdateStatusCancelled,
		"UNKNOWN_STATUS",
	}
	for _, s := range statuses {
		fn := GetStatusColor(s)
		if fn == nil {
			t.Errorf("GetStatusColor(%q) returned nil", s)
		}
		// Verify it is callable and returns a non-empty string
		if out := fn("test"); out == "" {
			t.Errorf("GetStatusColor(%q) color fn returned empty string", s)
		}
	}
}
