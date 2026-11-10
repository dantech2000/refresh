package fleet

import (
	"testing"

	"github.com/dantech2000/refresh/internal/apidoc"
)

// The one rule for the health of an add-on: its status, and then its
// health issues. An add-on with an issue is never Healthy.
func TestAddonHealth(t *testing.T) {
	issue := []Issue{{Code: "InsufficientNumberOfReplicas", Message: "the add-on has too few replicas"}}
	tests := []struct {
		status string
		issues []Issue
		want   apidoc.AddonHealth
	}{
		{status: "ACTIVE", want: apidoc.AddonHealthy},
		{status: "ACTIVE", issues: issue, want: apidoc.AddonFailed},
		{status: "DEGRADED", want: apidoc.AddonFailed},
		{status: "DEGRADED", issues: issue, want: apidoc.AddonFailed},
		{status: "CREATE_FAILED", want: apidoc.AddonFailed},
		{status: "UPDATE_FAILED", want: apidoc.AddonFailed},
		{status: "DELETE_FAILED", want: apidoc.AddonFailed},
		{status: "CREATING", want: apidoc.AddonInProgress},
		{status: "UPDATING", want: apidoc.AddonInProgress},
		// An update in flight is InProgress, also while the issue that it
		// repairs is still listed.
		{status: "UPDATING", issues: issue, want: apidoc.AddonInProgress},
		{status: "DELETING", want: apidoc.AddonInProgress},
		{status: "", want: apidoc.AddonUnknown},
		{status: "SOMETHING_NEW", issues: issue, want: apidoc.AddonUnknown},
	}
	for _, tt := range tests {
		a := Addon{Name: "coredns", State: Collected, Status: tt.status, Issues: tt.issues}
		if got := a.Health(); got != tt.want {
			t.Errorf("status %q with %d issue(s): health = %s, want %s", tt.status, len(tt.issues), got, tt.want)
		}
		// The document of the add-on has the same value.
		if got := AddonDoc(a).Health; got != tt.want {
			t.Errorf("status %q with %d issue(s): the document says %s, want %s", tt.status, len(tt.issues), got, tt.want)
		}
	}
}
