package clusterview

import (
	"fmt"

	"github.com/dantech2000/refresh/internal/render"
	"github.com/dantech2000/refresh/internal/services/status"
)

// autoUpgradeNote replaces the extended-support premium for a cluster whose
// upgrade policy is STANDARD: it never pays extended support.
const autoUpgradeNote = "auto-upgrades at end of standard support"

// supportToken renders an EKS support posture (tier + days remaining, and the
// extended-support premium when in extended) as a colored token: green for
// standard, a warning for extended, a failure for unsupported. Reuses the
// posture resolved by the shared status resolver (REF-145).
func supportToken(th *render.Theme, p *status.SupportPosture) string {
	if p == nil {
		return th.Paint(th.Pal.Dim, "unknown")
	}
	days := ""
	if p.DaysRemaining != nil {
		days = fmt.Sprintf(" (%dd)", *p.DaysRemaining)
	}
	switch p.Tier {
	case status.SupportStandard:
		txt := "standard" + days
		if p.AutoUpgradeAtStandardEnd {
			txt += " · " + autoUpgradeNote
		}
		return th.Paint(th.Pal.Green, txt)
	case status.SupportExtended:
		txt := "extended" + days
		switch {
		case p.AutoUpgradeAtStandardEnd:
			txt += " · " + autoUpgradeNote
		case p.ExtraCostUSDPerHour > 0:
			txt += fmt.Sprintf(" · +$%.2f/hr", p.ExtraCostUSDPerHour)
		}
		return th.Token(render.Warn, txt)
	case status.SupportUnsupported:
		return th.Token(render.Fail, "unsupported")
	default:
		return th.Paint(th.Pal.Dim, "unknown")
	}
}

// supportPlain is the uncolored form of a support posture for `-o plain`.
func supportPlain(p *status.SupportPosture) string {
	if p == nil {
		return "unknown"
	}
	days := ""
	if p.DaysRemaining != nil {
		days = fmt.Sprintf(" (%dd)", *p.DaysRemaining)
	}
	switch p.Tier {
	case status.SupportStandard:
		s := "standard" + days
		if p.AutoUpgradeAtStandardEnd {
			s += ", " + autoUpgradeNote
		}
		return s
	case status.SupportExtended:
		s := "extended" + days
		switch {
		case p.AutoUpgradeAtStandardEnd:
			s += ", " + autoUpgradeNote
		case p.ExtraCostUSDPerHour > 0:
			s += fmt.Sprintf(", +$%.2f/hr", p.ExtraCostUSDPerHour)
		}
		return s
	case status.SupportUnsupported:
		return "unsupported"
	default:
		return "unknown"
	}
}
