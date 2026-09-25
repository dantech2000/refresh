package tui

import (
	"image/color"
	"io"

	"github.com/dantech2000/refresh/internal/render"
	"github.com/dantech2000/refresh/internal/tui/state"
)

func rgb(c render.Color) color.Color { return color.RGBA{R: c.R, G: c.G, B: c.B, A: 0xff} }

// The TUI palette: Catppuccin Mocha, the same colors internal/render uses
// for the CLI, plus the background layers a full-screen view needs.
var (
	colText     = rgb(render.Mocha.Text)
	colSubtext  = rgb(render.Mocha.Subtext)
	colGreen    = rgb(render.Mocha.Green)
	colYellow   = rgb(render.Mocha.Yellow)
	colRed      = rgb(render.Mocha.Red)
	colTeal     = rgb(render.Mocha.Teal)
	colBlue     = rgb(render.Mocha.Blue)
	colMauve    = rgb(render.Mocha.Mauve)
	colPeach    = rgb(render.Mocha.Peach)
	colSky      = rgb(render.Mocha.Sky)
	colSurface1 = rgb(render.Mocha.Rule)

	// Catppuccin Mocha layers and overlay2. render.Mocha.Dim (overlay0) is
	// too faint for text on base, so secondary text uses overlay2.
	colBase     color.Color = color.RGBA{0x1e, 0x1e, 0x2e, 0xff}
	colMantle   color.Color = color.RGBA{0x18, 0x18, 0x25, 0xff}
	colCrust    color.Color = color.RGBA{0x11, 0x11, 0x1b, 0xff}
	colSurface0 color.Color = color.RGBA{0x31, 0x32, 0x44, 0xff}
	colDim      color.Color = color.RGBA{0x93, 0x99, 0xb2, 0xff}
)

// glyphs supplies the status glyphs: the same tokens as the CLI views, with
// the same ASCII fallback on a terminal without UTF-8.
var glyphs = render.Default(io.Discard)

// mark is the status glyph of an event level.
func mark(l state.Level) string {
	switch l {
	case state.LevelOK:
		return glyphs.Mark(render.Healthy)
	case state.LevelWarn:
		return glyphs.Mark(render.Warn)
	case state.LevelError:
		return glyphs.Mark(render.Fail)
	case state.LevelProgress:
		return glyphs.Mark(render.Progress)
	default:
		return glyphs.Mark(render.Neutral)
	}
}

// levelGlyph returns the status glyph of an event level in its color.
func levelGlyph(l state.Level) Seg { return fg(levelColor(l), mark(l)) }

// tok is a status token: glyph and label, both in the level's color.
func tok(l state.Level, label string) Seg { return fg(levelColor(l), mark(l)+" "+label) }

// heading is a section title with the section marker.
func heading(title string) string { return glyphs.SectionMark() + " " + title }

func levelColor(l state.Level) color.Color {
	switch l {
	case state.LevelOK:
		return colGreen
	case state.LevelWarn:
		return colYellow
	case state.LevelError:
		return colRed
	case state.LevelProgress:
		return colTeal
	default:
		return colDim
	}
}

func checkLevel(s state.CheckStatus) state.Level {
	switch s {
	case state.CheckPass:
		return state.LevelOK
	case state.CheckWarn:
		return state.LevelWarn
	case state.CheckFail:
		return state.LevelError
	case state.CheckRunning:
		return state.LevelProgress
	default:
		return state.LevelInfo
	}
}

func phaseLevel(s state.PhaseStatus) state.Level {
	switch s {
	case state.PhaseDone:
		return state.LevelOK
	case state.PhaseRunning:
		return state.LevelProgress
	case state.PhaseFailed:
		return state.LevelError
	case state.PhaseStopped:
		return state.LevelWarn
	default:
		return state.LevelInfo
	}
}

// phaseGlyph is levelGlyph with ○ for a phase not started.
func phaseGlyph(s state.PhaseStatus) Seg {
	if s == state.PhasePending || s == state.PhaseSkipped {
		return fg(colDim, glyphs.Mark(render.Unknown))
	}
	return levelGlyph(phaseLevel(s))
}
