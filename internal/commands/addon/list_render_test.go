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
		{Name: "vpc-cni", Version: "v1.18.3", Status: "ACTIVE", Health: addons.HealthPass},
		{Name: "coredns", Version: "v1.11.1", Status: "DEGRADED", Health: addons.HealthFail},
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
		"● PASS",
		"✗ DEGRADED",
		"✗ FAIL",
		"—", // empty health renders as a dim dash
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("addon list missing %q in:\n%s", want, joined)
		}
	}
}

func TestAddonListLines_ASCII(t *testing.T) {
	th := render.New(render.ColorNone, false)
	rows := []addons.AddonSummary{{Name: "vpc-cni", Version: "v1.18.3", Status: "ACTIVE", Health: addons.HealthPass}}
	joined := strings.Join(addonListLines(th, "prod", rows), "\n")
	if !strings.Contains(joined, "[OK] ACTIVE") || !strings.Contains(joined, "[OK] PASS") {
		t.Errorf("ASCII tokens missing:\n%s", joined)
	}
}
