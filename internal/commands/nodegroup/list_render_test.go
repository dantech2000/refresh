package nodegroup

import (
	"strings"
	"testing"

	"github.com/dantech2000/refresh/internal/render"
	nodegroupsvc "github.com/dantech2000/refresh/internal/services/nodegroup"
	"github.com/dantech2000/refresh/internal/types"
)

// sampleNodegroups represents the measured-readiness path: ReadyKnown is true,
// so the NODES column renders a real ready/desired fraction. (REF-130)
func sampleNodegroups() []nodegroupsvc.NodegroupSummary {
	return []nodegroupsvc.NodegroupSummary{
		{Name: "general", Status: "ACTIVE", InstanceType: "m6i.large", AMIStatus: types.AMILatest, ReadyNodes: 6, DesiredSize: 6, ReadyKnown: true},
		{Name: "spot", Status: "DEGRADED", InstanceType: "m6i.xlarge", AMIStatus: types.AMIOutdated, ReadyNodes: 1, DesiredSize: 2, ReadyKnown: true},
	}
}

func TestNodegroupListLines(t *testing.T) {
	th := render.New(render.ColorNone, true)
	joined := strings.Join(nodegroupListLines(th, "prod", sampleNodegroups()), "\n")

	if strings.Contains(joined, "\x1b") {
		t.Fatalf("ColorNone output contains ANSI escapes:\n%s", joined)
	}
	for _, want := range []string{
		"NODEGROUPS  prod · 2",
		"● ACTIVE",
		"✗ DEGRADED",                      // DEGRADED -> fail token
		"6/6",                             // measured: ready == desired
		"1/2",                             // measured: 1 ready of 2 desired
		"● " + types.AMILatest.String(),   // up-to-date AMI -> healthy token
		"▲ " + types.AMIOutdated.String(), // outdated AMI -> warn token
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("nodegroup list missing %q in:\n%s", want, joined)
		}
	}
}

// REF-130: without measured readiness (ReadyKnown=false) the NODES column shows
// the desired count only — never a fabricated ready/desired fraction.
func TestNodegroupListLines_ReadinessUnknown(t *testing.T) {
	th := render.New(render.ColorNone, true)
	items := []nodegroupsvc.NodegroupSummary{
		{Name: "general", Status: "ACTIVE", InstanceType: "m6i.large", AMIStatus: types.AMILatest, DesiredSize: 42},
	}
	joined := strings.Join(nodegroupListLines(th, "prod", items), "\n")

	if !strings.Contains(joined, "42") {
		t.Errorf("expected desired count 42 in NODES cell:\n%s", joined)
	}
	if strings.Contains(joined, "/42") || strings.Contains(joined, "0/42") {
		t.Errorf("unmeasured readiness must not render a fraction:\n%s", joined)
	}
}

func TestNodegroupListLines_ASCII(t *testing.T) {
	th := render.New(render.ColorNone, false)
	joined := strings.Join(nodegroupListLines(th, "prod", sampleNodegroups()), "\n")
	if !strings.Contains(joined, "[OK] ACTIVE") || !strings.Contains(joined, "[X] DEGRADED") {
		t.Errorf("ASCII tokens missing:\n%s", joined)
	}
	if strings.ContainsAny(joined, "●▲✗") {
		t.Errorf("ASCII fallback still has Unicode glyphs:\n%s", joined)
	}
}
