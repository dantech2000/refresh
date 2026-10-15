package view

import (
	"image/color"
	"strconv"

	"github.com/charmbracelet/x/ansi"
)

// Role names what a piece of text is on a full-screen view. A caller
// names the role and never a color, so one theme decides every color.
type Role int

// The roles.
const (
	RoleNone    Role = iota // the default color of the terminal
	RoleText                // body text
	RoleSubtext             // secondary text
	RoleDim                 // a column title, a hint
	RoleHealthy
	RoleWarn
	RoleFail
	RoleProgress
	RoleAccent // a heading, a selection marker
	RoleInfo   // a name that is not a status
	RoleRule   // a border, a rule
	RoleBase   // the background of a screen
	RoleMantle // the background of a bar
	RoleCrust  // the background of the key bar, and the text on a badge
	RoleSurface
)

// The colors of the roles that the status palette does not have: the
// Catppuccin Mocha layers, overlay2 for secondary text, and sky.
var (
	colorText    = rgb{205, 214, 244}
	colorSubtext = rgb{166, 173, 200}
	colorOverlay = rgb{147, 153, 178}
	colorSky     = rgb{137, 220, 235}
	colorRule    = rgb{69, 71, 90}
	colorBase    = rgb{30, 30, 46}
	colorMantle  = rgb{24, 24, 37}
	colorCrust   = rgb{17, 17, 27}
	colorSurface = rgb{49, 50, 68}
)

// Color returns the color of a role at the depth of the theme. It is
// false for RoleNone and with NoColor: nothing is painted then.
func (t Theme) Color(r Role) (color.Color, bool) {
	var c rgb
	switch r {
	case RoleText:
		c = colorText
	case RoleSubtext:
		c = colorSubtext
	case RoleDim:
		c = colorOverlay
	case RoleHealthy:
		c = colorGreen
	case RoleWarn:
		c = colorYellow
	case RoleFail:
		c = colorRed
	case RoleProgress:
		c = colorTeal
	case RoleAccent:
		c = colorMauve
	case RoleInfo:
		c = colorSky
	case RoleRule:
		c = colorRule
	case RoleBase:
		c = colorBase
	case RoleMantle:
		c = colorMantle
	case RoleCrust:
		c = colorCrust
	case RoleSurface:
		c = colorSurface
	default:
		return nil, false
	}
	switch t.Depth {
	case TrueColor:
		return color.RGBA{R: c.r, G: c.g, B: c.b, A: 0xff}, true
	case Color256:
		return ansi.IndexedColor(to256(c)), true //nolint:gosec // to256 returns 16 to 255
	}
	return nil, false
}

// SGR returns the escape sequences that paint a role as the foreground
// and as the background of a text, at the depth of the theme. Both are ""
// for RoleNone and with NoColor. A frame renders thousands of segments;
// the codes are computed once per role and written as they are.
func (t Theme) SGR(r Role) (fg, bg string) {
	c, ok := t.Color(r)
	if !ok {
		return "", ""
	}
	var code string
	switch v := c.(type) {
	case color.RGBA:
		code = "2;" + strconv.Itoa(int(v.R)) + ";" + strconv.Itoa(int(v.G)) + ";" + strconv.Itoa(int(v.B))
	case ansi.IndexedColor:
		code = "5;" + strconv.Itoa(int(v))
	default:
		return "", ""
	}
	return "\x1b[38;" + code + "m", "\x1b[48;" + code + "m"
}

// RoleOf returns the role that paints a status.
func RoleOf(s Status) Role {
	switch s {
	case Healthy:
		return RoleHealthy
	case Warn:
		return RoleWarn
	case Fail:
		return RoleFail
	case Progress:
		return RoleProgress
	case Unknown:
		return RoleDim
	}
	return RoleSubtext
}

// Border is the set of box-drawing strings of a theme.
type Border struct {
	TopLeft, TopRight, BottomLeft, BottomRight string
	Horizontal, Vertical                       string
	// Marker is the selection marker, and Ellipsis the end of a cut text.
	// Both are one cell, so a cut line keeps its width in either set.
	Marker, Ellipsis string
	// Full and Empty are the cells of a bar.
	Full, Empty string
}

// Border returns the box-drawing set of the theme: Unicode, or ASCII
// without it, so a frame reads the same in both.
func (t Theme) Border() Border {
	if !t.Unicode {
		return Border{TopLeft: "+", TopRight: "+", BottomLeft: "+", BottomRight: "+", Horizontal: "-", Vertical: "|", Marker: ">", Ellipsis: "~", Full: "#", Empty: "."}
	}
	return Border{TopLeft: "╭", TopRight: "╮", BottomLeft: "╰", BottomRight: "╯", Horizontal: "─", Vertical: "│", Marker: "▶", Ellipsis: "…", Full: "█", Empty: "░"}
}

// Mark returns the glyph of a status without color: a frame paints it
// through a role.
func (t Theme) Mark(s Status) string {
	if s == NoStatus {
		return ""
	}
	glyph, ascii, _ := s.parts()
	if !t.Unicode {
		return ascii
	}
	return glyph
}
