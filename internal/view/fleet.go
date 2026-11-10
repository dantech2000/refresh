package view

import "github.com/dantech2000/refresh/internal/fleet"

// StandingStatus returns the status that draws the standing of a cluster:
// the glyph of a row of the fleet.
func StandingStatus(s fleet.Standing) Status {
	switch s {
	case fleet.StandingCurrent:
		return Healthy
	case fleet.StandingIncomplete:
		return Unknown
	case fleet.StandingNeedsAttention:
		return Warn
	case fleet.StandingUnsupported:
		return Fail
	}
	return Unknown
}
