package view

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/dantech2000/refresh/internal/apidoc"
)

// The cells that several commands show for the same data: a resource has
// one text in every command, as it has one shape in every document.

// UnknownCell is the text of a cell whose data could not be read.
const UnknownCell = "unknown"

// OrUnknown returns value, or "unknown" when value is empty because the
// thing it belongs to was not read: an empty value of a thing that was
// read stays empty.
func OrUnknown(incomplete bool, value string) string {
	if value == "" && incomplete {
		return UnknownCell
	}
	return value
}

// dateLayout is how a describe view prints a time.
const dateLayout = "2006-01-02 15:04 UTC"

// Time returns t for a describe view, in UTC to the minute, or "" for the
// zero time.
func Time(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(dateLayout)
}

// SupportCell returns the support of a cluster as a short cell: the tier,
// the days left while a window is open, and the premium of extended
// support, such as "standard (61d)" or "extended (40d) +$0.50/hr". A "*"
// marks dates that refresh ships with, because EKS did not report them.
// It returns "" when the support was not read.
func SupportCell(s *apidoc.Support) string {
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
	if s.ExtendedSupportPremiumUSDPerHour > 0 {
		text += fmt.Sprintf(" +$%.2f/hr", s.ExtendedSupportPremiumUSDPerHour)
	}
	return text
}

// SupportStatus returns the status that draws the support of a cluster.
func SupportStatus(s *apidoc.Support) Status {
	switch s.Tier {
	case apidoc.SupportStandard:
		return Healthy
	case apidoc.SupportExtended:
		return Warn
	case apidoc.SupportUnsupported:
		return Fail
	}
	return Unknown
}

// NodesCell returns the desired size of a nodegroup as a cell, with the
// Ready count before it when it was read: "5/6". A nodegroup without sizes
// was not read, or EKS did not return them: unknown.
func NodesCell(n apidoc.Nodegroup) string {
	if n.Scaling == nil {
		return UnknownCell
	}
	desired := strconv.Itoa(int(n.Scaling.Desired))
	if n.ReadyNodes != nil {
		return strconv.Itoa(*n.ReadyNodes) + "/" + desired
	}
	return desired
}

// BehindStatus returns the status that draws the version of a nodegroup:
// a warning when it is behind the control plane.
func BehindStatus(n apidoc.Nodegroup) Status {
	if n.VersionBehind != nil && *n.VersionBehind {
		return Warn
	}
	return NoStatus
}

// AddonHealthStatus returns the status that draws the health of an
// add-on.
func AddonHealthStatus(h apidoc.AddonHealth) Status {
	switch h {
	case apidoc.AddonHealthy:
		return Healthy
	case apidoc.AddonFailed:
		return Fail
	case apidoc.AddonInProgress:
		return Progress
	}
	return Unknown
}

// HealthItems returns one item for each health result: its outcome and its
// message.
func HealthItems(results []apidoc.HealthResult) []Item {
	items := make([]Item, len(results))
	for i, r := range results {
		items[i] = Item{Name: r.Name, Fields: []Field{
			{Name: "outcome", Value: string(r.Outcome), Status: HealthStatus(r.Outcome)},
			{Name: "message", Value: r.Message},
		}}
	}
	return items
}

// Plural returns n and noun, with an "s" unless n is 1.
func Plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return strconv.Itoa(n) + " " + noun + "s"
}
