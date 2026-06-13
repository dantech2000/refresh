package addon

import (
	"strings"
	"testing"

	"github.com/dantech2000/refresh/internal/render"
	"github.com/dantech2000/refresh/internal/services/addons"
)

func TestAddonListLines(t *testing.T) {
	th := render.New(render.ColorNone, true)
	rows := []addons.AddonSummary{
		{Name: "vpc-cni", Version: "v1.18.3", Status: "ACTIVE", Health: "Healthy"},
		{Name: "coredns", Version: "v1.11.1", Status: "DEGRADED", Health: "Degraded"},
		{Name: "kube-proxy", Version: "v1.30.0", Status: "ACTIVE", Health: ""},
	}
	joined := strings.Join(addonListLines(th, "prod", rows), "\n")

	if strings.Contains(joined, "\x1b") {
		t.Fatalf("ColorNone output contains ANSI escapes:\n%s", joined)
	}
	for _, want := range []string{
		"ADD-ONS  prod · 3",
		"vpc-cni",
		"● ACTIVE",
		"● Healthy",
		"✗ DEGRADED",
		"✗ Degraded",
		"—", // empty health renders as a dim dash
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("addon list missing %q in:\n%s", want, joined)
		}
	}
}

func TestAddonListLines_ASCII(t *testing.T) {
	th := render.New(render.ColorNone, false)
	rows := []addons.AddonSummary{{Name: "vpc-cni", Version: "v1.18.3", Status: "ACTIVE", Health: "Healthy"}}
	joined := strings.Join(addonListLines(th, "prod", rows), "\n")
	if !strings.Contains(joined, "[OK] ACTIVE") || !strings.Contains(joined, "[OK] Healthy") {
		t.Errorf("ASCII tokens missing:\n%s", joined)
	}
}
