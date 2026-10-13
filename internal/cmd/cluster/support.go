package cluster

import (
	"strconv"
	"strings"

	"github.com/dantech2000/refresh/internal/apidoc"
	"github.com/dantech2000/refresh/internal/view"
)

// supportText returns the support of a cluster as a short cell: the tier,
// and the days left while a window is open, such as "standard (61d)". A
// "*" marks dates that refresh ships with, because EKS did not report
// them. It returns "" when the support was not read.
func supportText(s *apidoc.Support) string {
	if s == nil {
		return ""
	}
	text := strings.ToLower(string(s.Tier))
	if s.DaysRemaining > 0 {
		text += " (" + strconv.Itoa(s.DaysRemaining) + "d)"
	}
	if s.BuiltIn {
		text += "*"
	}
	return text
}

// supportStatus returns the status that draws the support of a cluster.
func supportStatus(s *apidoc.Support) view.Status {
	switch s.Tier {
	case apidoc.SupportStandard:
		return view.Healthy
	case apidoc.SupportExtended:
		return view.Warn
	case apidoc.SupportUnsupported:
		return view.Fail
	}
	return view.Unknown
}
