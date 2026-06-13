package statusview

import (
	"strings"
	"testing"

	"github.com/dantech2000/refresh/internal/render"
	statussvc "github.com/dantech2000/refresh/internal/services/status"
)

func iptr(i int) *int { return &i }

func sampleFleet() []statussvc.ClusterStatus {
	return []statussvc.ClusterStatus{
		{ // current: standard support, nothing stale
			Name: "prod-east", Region: "us-east-1", Version: "1.32",
			Support:        statussvc.SupportPosture{Tier: statussvc.SupportStandard, DaysRemaining: iptr(200)},
			Compute:        statussvc.ComputeManaged,
			NodegroupCount: 3,
		},
		{ // warning: standard support but an addon behind
			Name: "staging", Region: "us-west-2", Version: "1.31",
			Support:        statussvc.SupportPosture{Tier: statussvc.SupportStandard, DaysRemaining: iptr(320)},
			Compute:        statussvc.ComputeManaged,
			NodegroupCount: 2,
			AddonsBehind:   statussvc.AddonsBehindSummary{Total: 4, Behind: 1, Names: []string{"kube-proxy"}},
		},
		{ // failure: unsupported EKS + stale AMIs
			Name: "data-eu", Region: "eu-central-1", Version: "1.29",
			Support:        statussvc.SupportPosture{Tier: statussvc.SupportUnsupported},
			Compute:        statussvc.ComputeManaged,
			NodegroupCount: 5,
			StaleAMI:       statussvc.StaleAMISummary{Total: 5, Behind: 5, OldestDays: iptr(47)},
			AddonsBehind:   statussvc.AddonsBehindSummary{Total: 4, Behind: 2, Names: []string{"vpc-cni", "coredns"}},
		},
	}
}

func TestFleetLines_Pretty(t *testing.T) {
	th := render.New(render.ColorNone, true) // deterministic: glyphs, no ANSI
	lines := fleetLines(th, sampleFleet(), 0)
	joined := strings.Join(lines, "\n")

	// No color escapes leak under ColorNone (additive-color contract).
	if strings.Contains(joined, "\x1b") {
		t.Fatalf("ColorNone output contains ANSI escapes:\n%s", joined)
	}

	// Header + summary chips (chips have no column padding, so exact-match).
	if lines[0] != "FLEET  3 clusters · 3 region(s)" {
		t.Errorf("header = %q", lines[0])
	}
	if lines[2] != "● 1 current   ▲ 1 need attention   ✗ 1 unsupported" {
		t.Errorf("chips = %q", lines[2])
	}

	// Each cluster's row carries its status glyph (its own column, 2-space gap)
	// + the salient facts.
	mustContain(t, joined, "●  prod-east")
	mustContain(t, joined, "▲  staging")
	mustContain(t, joined, "✗  data-eu")
	mustContain(t, joined, "5/5 (47d)")           // stale AMI cell
	mustContain(t, joined, "2 (vpc-cni,coredns)") // addons-behind cell
	mustContain(t, joined, "standard (200d)")     // support cell

	// Footer aggregates and the next-step hint point at the worst cluster.
	mustContain(t, joined, "3 clusters · 5 stale nodegroups · 3 addons behind · 1 extended/unsupported")
	mustContain(t, joined, "refresh cluster upgrade-check -c data-eu")
}

func TestFleetLines_ASCIIFallback(t *testing.T) {
	th := render.New(render.ColorNone, false) // non-UTF-8 terminal
	joined := strings.Join(fleetLines(th, sampleFleet(), 0), "\n")
	// Glyphs degrade to ASCII tokens; meaning preserved without color or Unicode.
	mustContain(t, joined, "[X] data-eu")
	mustContain(t, joined, "[OK] 1 current")
	if strings.ContainsAny(joined, "●▲✗") {
		t.Errorf("ASCII fallback still contains Unicode glyphs:\n%s", joined)
	}
}

func TestFleetLines_AllHealthy(t *testing.T) {
	th := render.New(render.ColorNone, true)
	healthy := []statussvc.ClusterStatus{{
		Name: "ok", Region: "us-east-1", Version: "1.33",
		Support: statussvc.SupportPosture{Tier: statussvc.SupportStandard, DaysRemaining: iptr(300)},
		Compute: statussvc.ComputeManaged, NodegroupCount: 1,
	}}
	lines := fleetLines(th, healthy, 0)
	joined := strings.Join(lines, "\n")
	// Chips show only the "current" count; no warn/fail chips.
	if lines[2] != "● 1 current" {
		t.Errorf("chips = %q, want %q", lines[2], "● 1 current")
	}
	// No next-step hint when everything is current.
	if strings.Contains(joined, "upgrade-check") {
		t.Errorf("healthy fleet should not emit a hint:\n%s", joined)
	}
}

func mustContain(t *testing.T, haystack, needle string) {
	t.Helper()
	if !strings.Contains(haystack, needle) {
		t.Errorf("output missing %q in:\n%s", needle, haystack)
	}
}
