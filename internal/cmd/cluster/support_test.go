package cluster

import (
	"testing"
	"time"

	"github.com/dantech2000/refresh/internal/apidoc"
)

// A cluster in extended support shows the premium over standard support in
// the subtitle of upgrade-check and on the support line of describe. With
// no premium, as under a STANDARD upgrade policy, neither shows a price.
func TestSupport_Premium(t *testing.T) {
	end := time.Date(2027, 6, 1, 0, 0, 0, 0, time.UTC)
	paid := &apidoc.Support{Tier: apidoc.SupportExtended, ExtendedUntil: end, DaysRemaining: 40, ExtendedSupportPremiumUSDPerHour: 0.5}
	auto := &apidoc.Support{Tier: apidoc.SupportExtended, ExtendedUntil: end, DaysRemaining: 40, AutoUpgrade: true}
	tests := []struct {
		name, got, want string
	}{
		{"text", supportText(paid), "extended (40d) +$0.50/hr"},
		{"text, auto-upgrade", supportText(auto), "extended (40d)"},
		{"line", supportLine(paid), "extended until 2027-06-01 (40 days), +$0.50/hr over standard support"},
		{"line, auto-upgrade", supportLine(auto), "extended until 2027-06-01 (40 days), at a higher price"},
	}
	for _, tt := range tests {
		if tt.got != tt.want {
			t.Errorf("%s = %q, want %q", tt.name, tt.got, tt.want)
		}
	}
}
