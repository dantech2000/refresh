package statusview

import (
	"testing"
	"time"

	statussvc "github.com/dantech2000/refresh/internal/services/status"
)

// The -o plain VERSION and SUPPORT cells in every tier, with the end dates
// and the "*" fallback marker the human table leaves out.
func TestPlainCells_VersionAndSupport(t *testing.T) {
	std := time.Date(2026, 11, 26, 0, 0, 0, 0, time.UTC)
	ext := time.Date(2027, 11, 26, 0, 0, 0, 0, time.UTC)
	days := 40

	if got := versionCell(statussvc.ClusterStatus{}); got != "unknown" {
		t.Errorf("empty version = %q, want unknown", got)
	}
	if got := versionCell(statussvc.ClusterStatus{Version: "1.33"}); got != "1.33" {
		t.Errorf("version = %q, want 1.33", got)
	}

	for _, tc := range []struct {
		posture statussvc.SupportPosture
		want    string
	}{
		{statussvc.SupportPosture{Tier: statussvc.SupportStandard, StandardUntil: &std, DaysRemaining: &days}, "standard until 2026-11-26 (40d)"},
		{statussvc.SupportPosture{Tier: statussvc.SupportStandard, Fallback: true}, "standard*"},
		{statussvc.SupportPosture{Tier: statussvc.SupportExtended, ExtendedUntil: &ext, ExtraCostUSDPerHour: 0.6}, "extended until 2027-11-26 +$0.60/hr"},
		{statussvc.SupportPosture{Tier: statussvc.SupportExtended, Fallback: true}, "extended*"},
		{statussvc.SupportPosture{Tier: statussvc.SupportStandard, DaysRemaining: &days, AutoUpgradeAtStandardEnd: true}, "standard (40d) auto-upgrades at end of standard support"},
		{statussvc.SupportPosture{Tier: statussvc.SupportExtended, AutoUpgradeAtStandardEnd: true}, "extended auto-upgrades at end of standard support"},
		{statussvc.SupportPosture{Tier: statussvc.SupportUnsupported, Fallback: true}, "unsupported*"},
		{statussvc.SupportPosture{Tier: statussvc.SupportUnknown, Fallback: true}, "unknown"},
	} {
		if got := supportCell(tc.posture); got != tc.want {
			t.Errorf("supportCell(%+v) = %q, want %q", tc.posture, got, tc.want)
		}
	}
}
