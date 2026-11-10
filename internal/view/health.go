package view

import (
	"strings"

	"github.com/dantech2000/refresh/internal/apidoc"
)

// HealthStatus returns the status that draws an outcome. A Skipped check
// has none: it is drawn dim, with no glyph.
func HealthStatus(o apidoc.HealthOutcome) Status {
	switch o {
	case apidoc.HealthPass:
		return Healthy
	case apidoc.HealthWarn:
		return Warn
	case apidoc.HealthFail:
		return Fail
	case apidoc.HealthUnreadable:
		return Unknown
	}
	return NoStatus
}

// ReadinessStatus returns the status that draws a readiness verdict.
func ReadinessStatus(r apidoc.Readiness) Status {
	switch r {
	case apidoc.ReadinessReady:
		return Healthy
	case apidoc.ReadinessReview:
		return Warn
	case apidoc.ReadinessBlocked:
		return Fail
	}
	return Unknown
}

// InsightStatus returns the status that draws the status of an EKS
// insight: PASSING, WARNING, ERROR, or another value, which is unknown.
func InsightStatus(status string) Status {
	switch status {
	case "PASSING":
		return Healthy
	case "WARNING":
		return Warn
	case "ERROR":
		return Fail
	}
	return Unknown
}

// HealthLines draws one line per result: the glyph of its outcome, the
// name padded to the widest, and the message. A Skipped result is dim,
// with no glyph.
func HealthLines(th Theme, results []apidoc.HealthResult) []string {
	width, glyphs := 0, 0
	for _, r := range results {
		width = max(width, Width(r.Name))
		glyphs = max(glyphs, visibleWidth(th.Glyph(HealthStatus(r.Outcome))))
	}
	out := make([]string, 0, len(results))
	for _, r := range results {
		name := pad(r.Name, width, false)
		if r.Outcome == apidoc.HealthSkipped {
			// The space of a glyph, so the names stay in one column.
			out = append(out, indent+strings.Repeat(" ", glyphs+1)+th.Dim(name+columnGap+r.Message))
			continue
		}
		glyph := th.Glyph(HealthStatus(r.Outcome))
		glyph += strings.Repeat(" ", glyphs-visibleWidth(glyph))
		out = append(out, indent+glyph+" "+name+columnGap+r.Message)
	}
	return out
}
