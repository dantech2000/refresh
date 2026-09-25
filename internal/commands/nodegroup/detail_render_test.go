package nodegroup

import (
	"strings"
	"testing"

	"github.com/dantech2000/refresh/internal/render"
	nodegroupsvc "github.com/dantech2000/refresh/internal/services/nodegroup"
	"github.com/dantech2000/refresh/internal/types"
)

// One AMI vocabulary for every view: Updating is in progress (not unknown),
// Outdated is a warning (not a failure), and Custom is neutral.
func TestAMIToken(t *testing.T) {
	cases := map[types.AMIStatus][2]string{
		types.AMILatest:   {"● Latest", "[OK] Latest"},
		types.AMIOutdated: {"▲ Outdated", "[!] Outdated"},
		types.AMIUpdating: {"◷ Updating", "[~] Updating"},
		types.AMICustom:   {"• Custom", "- Custom"},
		types.AMIUnknown:  {"○ Unknown", "[?] Unknown"},
	}
	for s, want := range cases {
		if got := amiToken(render.New(render.ColorNone, true), s); got != want[0] {
			t.Errorf("%v: token = %q, want %q", s, got, want[0])
		}
		if got := amiToken(render.New(render.ColorNone, false), s); got != want[1] {
			t.Errorf("%v: ASCII token = %q, want %q", s, got, want[1])
		}
	}
}

// The describe view uses the same AMI token as the list, and reads without
// color or Unicode.
func TestNodegroupDetailLines_AMIToken(t *testing.T) {
	d := &nodegroupsvc.NodegroupDetails{Name: "workers", Status: "UPDATING", AMIStatus: types.AMIUpdating}
	joined := strings.Join(nodegroupDetailLines(render.New(render.ColorNone, false), d, 0), "\n")
	for _, want := range []string{"workers   [~] UPDATING", "ami status   [~] Updating", "> OVERVIEW"} {
		if !strings.Contains(joined, want) {
			t.Errorf("describe view missing %q:\n%s", want, joined)
		}
	}
	if strings.ContainsAny(joined, "●▲◷✗○▸\x1b") {
		t.Errorf("ASCII describe view has Unicode glyphs or ANSI:\n%s", joined)
	}
}
