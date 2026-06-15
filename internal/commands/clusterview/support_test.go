package clusterview

import (
	"strings"
	"testing"

	"github.com/dantech2000/refresh/internal/render"
	clustersvc "github.com/dantech2000/refresh/internal/services/cluster"
	"github.com/dantech2000/refresh/internal/services/status"
)

func intp(i int) *int { return &i }

func TestSupportFormatters(t *testing.T) {
	th := render.New(render.ColorNone, true)

	std := &status.SupportPosture{Tier: status.SupportStandard, DaysRemaining: intp(200)}
	if got := supportPlain(std); got != "standard (200d)" {
		t.Errorf("standard plain = %q, want %q", got, "standard (200d)")
	}
	if got := supportToken(th, std); !strings.Contains(got, "standard (200d)") {
		t.Errorf("standard token = %q", got)
	}

	ext := &status.SupportPosture{Tier: status.SupportExtended, DaysRemaining: intp(90), ExtraCostUSDPerHour: 0.50}
	if got := supportPlain(ext); got != "extended (90d), +$0.50/hr" {
		t.Errorf("extended plain = %q", got)
	}
	if got := supportToken(th, ext); !strings.Contains(got, "extended (90d)") || !strings.Contains(got, "+$0.50/hr") {
		t.Errorf("extended token = %q", got)
	}

	if got := supportPlain(&status.SupportPosture{Tier: status.SupportUnsupported}); got != "unsupported" {
		t.Errorf("unsupported plain = %q", got)
	}
	if got := supportPlain(nil); got != "unknown" {
		t.Errorf("nil plain = %q, want unknown", got)
	}
}

func TestUpgradeCheckLines_Support(t *testing.T) {
	th := render.New(render.ColorNone, true)
	rpt := &clustersvc.UpgradeReport{
		Cluster: "prod",
		Support: &status.SupportPosture{Tier: status.SupportStandard, DaysRemaining: intp(200)},
		Skew:    clustersvc.SkewReport{ControlPlaneVersion: "1.32"},
	}
	joined := strings.Join(upgradeCheckLines(th, rpt), "\n")
	if !strings.Contains(joined, "support  standard (200d)") {
		t.Errorf("upgrade-check missing support line in:\n%s", joined)
	}

	// Nil support → no support line (back-compat with existing reports).
	noSup := &clustersvc.UpgradeReport{Cluster: "prod", Skew: clustersvc.SkewReport{ControlPlaneVersion: "1.32"}}
	if got := strings.Join(upgradeCheckLines(th, noSup), "\n"); strings.Contains(got, "support  ") {
		t.Errorf("nil support should render no support line:\n%s", got)
	}
}

func TestClusterDetailLines_Support(t *testing.T) {
	th := render.New(render.ColorNone, true)
	d := sampleDetails()
	d.Support = &status.SupportPosture{Tier: status.SupportExtended, DaysRemaining: intp(45), ExtraCostUSDPerHour: 0.50}
	joined := strings.Join(clusterDetailLines(th, d, 0), "\n")
	if !strings.Contains(joined, "support") || !strings.Contains(joined, "extended (45d)") {
		t.Errorf("describe missing support KV in:\n%s", joined)
	}
}
