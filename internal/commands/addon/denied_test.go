package addon

import (
	"strings"
	"testing"

	"github.com/dantech2000/refresh/internal/mocks/fakeaws"
	"github.com/dantech2000/refresh/internal/ui"
)

// Denied UpdateAddon calls did not start: NOT STARTED, the action to grant
// once, and an exit message that does not call it missing data.
func TestUpdateAll_DeniedStartSaysWhatToGrant(t *testing.T) {
	withPrompt(t, true, "y")
	fakeaws.New(t, addonCluster(
		&fakeaws.Addon{Name: "coredns", Version: "v1.11.1", Available: []string{"v1.11.4", "v1.11.1"}, UpdateError: "AccessDeniedException"},
		&fakeaws.Addon{Name: "vpc-cni", Version: "v1.18.0", Available: []string{"v1.19.0", "v1.18.0"}, UpdateError: "AccessDeniedException"},
	))
	stdout, _, err := runAddon(t, "update", "prod", "--all", "--yes")
	if err == nil || !strings.Contains(err.Error(), "2 update(s) could not start (2 addons)") {
		t.Fatalf("err = %v", err)
	}
	out := ui.StripANSI(stdout)
	if !strings.Contains(out, "NOT STARTED") || strings.Count(out, "Grant eks:UpdateAddon to this identity") != 1 || strings.Contains(out, "INCOMPLETE DATA") {
		t.Fatalf("stdout:\n%s", out)
	}
}
