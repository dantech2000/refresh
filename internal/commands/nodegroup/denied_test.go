package nodegroup

import (
	"strings"
	"testing"

	"github.com/dantech2000/refresh/internal/mocks/fakeaws"
	"github.com/dantech2000/refresh/internal/ui"
)

// A denied UpdateNodegroupVersion did not start: the table view lists it
// under NOT STARTED with the action to grant, not as missing data.
func TestUpdate_DeniedStartSaysWhatToGrant(t *testing.T) {
	withTerminal(t, true)
	withScalePrompt(t, true, "y")
	fakeaws.New(t, prodCluster(&fakeaws.Nodegroup{Name: "web", Version: "1.31", UpdateError: "AccessDeniedException"}))
	stdout, _, err := runNodegroup(t, "update", "prod", "web", "--skip-health-check", "--yes", "--reroll")
	if code := exitCodeOf(err); code != 4 || !strings.Contains(err.Error(), "1 update(s) could not start (1 nodegroup)") {
		t.Fatalf("exit %d, err = %v", code, err)
	}
	out := ui.StripANSI(stdout)
	if !strings.Contains(out, "NOT STARTED") || !strings.Contains(out, "Grant eks:UpdateNodegroupVersion to this identity") || strings.Contains(out, "INCOMPLETE DATA") {
		t.Fatalf("stdout:\n%s", out)
	}
}

