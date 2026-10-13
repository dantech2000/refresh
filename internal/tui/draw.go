package tui

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
	"github.com/rivo/uniseg"

	"github.com/dantech2000/refresh/internal/view"
)

// Seg is a run of text with one role. Screens build Lines of Segs and
// render them last, so widths, truncation, and backgrounds are computed on
// plain text and every cell gets an explicit background.
type Seg struct {
	Text string
	Role view.Role
	// BG is the background role. The zero value takes the background of
	// the row.
	BG   view.Role
	Bold bool
}

// Line is one terminal row.
type Line []Seg

// Block is a rectangle of rows.
type Block []Line

// Segment helpers keep screen code short.
func tx(s string) Seg                       { return Seg{Text: s, Role: view.RoleText} }
func role(r view.Role, s string) Seg        { return Seg{Text: s, Role: r} }
func bold(r view.Role, s string) Seg        { return Seg{Text: s, Role: r, Bold: true} }
func dim(s string) Seg                      { return Seg{Text: s, Role: view.RoleDim} }
func sub(s string) Seg                      { return Seg{Text: s, Role: view.RoleSubtext} }
func sp(n int) Seg                          { return Seg{Text: strings.Repeat(" ", max(0, n))} }
func statusSeg(s view.Status, t string) Seg { return Seg{Text: t, Role: view.RoleOf(s)} }

// chip is a key hint: the key on a raised background.
func chip(k string) Seg { return Seg{Text: " " + k + " ", Role: view.RoleText, BG: view.RoleSurface} }

// badge is a label on a solid color.
func badge(r view.Role, s string) Seg {
	return Seg{Text: " " + s + " ", Role: view.RoleCrust, BG: r, Bold: true}
}

func width(s string) int { return ansi.StringWidth(s) }

// Width is the line's width in cells.
func (l Line) Width() int {
	n := 0
	for _, s := range l {
		n += width(s.Text)
	}
	return n
}

// Fit truncates the line to w cells, ending in an ellipsis when cut, or
// pads it with spaces to exactly w.
func (l Line) Fit(w int) Line {
	return l.fit(w, "…")
}

func (l Line) fit(w int, ellipsis string) Line {
	if w <= 0 {
		return nil
	}
	if lw := l.Width(); lw <= w {
		if lw == w {
			return l
		}
		return append(append(Line(nil), l...), sp(w-lw))
	}
	ew := width(ellipsis)
	if w <= ew {
		return l.Cut(0, w)
	}
	out := l.Cut(0, w-ew)
	last := Seg{Text: ellipsis, Role: view.RoleDim}
	if len(out) > 0 {
		last.BG = out[len(out)-1].BG
	}
	return append(out, last).fit(w, ellipsis)
}

// Cut returns the cells [from, to) of the line. It splits text only
// between graphemes, so it never breaks one apart; a wide grapheme that
// straddles an edge becomes spaces.
func (l Line) Cut(from, to int) Line {
	var out Line
	pos := 0
	for _, s := range l {
		sw := width(s.Text)
		if pos+sw <= from || pos >= to {
			pos += sw
			continue
		}
		if pos >= from && pos+sw <= to {
			out = append(out, s)
			pos += sw
			continue
		}
		var b strings.Builder
		rest, st := s.Text, -1
		for rest != "" {
			var g string
			g, rest, _, st = uniseg.FirstGraphemeClusterInString(rest, st)
			gw := width(g)
			switch {
			case pos >= from && pos+gw <= to:
				b.WriteString(g)
			case pos < to && pos+gw > from:
				// Partly inside: pad the visible cells.
				b.WriteString(strings.Repeat(" ", min(pos+gw, to)-max(pos, from)))
			}
			pos += gw
		}
		c := s
		c.Text = b.String()
		out = append(out, c)
	}
	return out
}

// WithBG sets the background of every segment that has none.
func (l Line) WithBG(bg view.Role) Line {
	out := make(Line, len(l))
	for i, s := range l {
		if s.BG == view.RoleNone {
			s.BG = bg
		}
		out[i] = s
	}
	return out
}

// painter holds the escape codes of every role of a theme, computed once:
// a frame renders thousands of segments.
type painter struct {
	fg, bg [view.RoleSurface + 1]string
	color  bool
}

func newPainter(th view.Theme) *painter {
	p := &painter{color: th.Depth != view.NoColor}
	for r := view.RoleNone; r <= view.RoleSurface; r++ {
		p.fg[r], p.bg[r] = th.SGR(r)
	}
	return p
}

// Render styles the line with p; segments with no background get bg.
// Without color nothing is styled, so a frame without color is its plain
// text.
func (l Line) Render(p *painter, bg view.Role) string {
	var b strings.Builder
	for _, s := range l {
		if s.Text == "" {
			continue
		}
		if !p.color {
			b.WriteString(s.Text)
			continue
		}
		segBG := s.BG
		if segBG == view.RoleNone {
			segBG = bg
		}
		b.WriteString(p.fg[s.Role])
		b.WriteString(p.bg[segBG])
		if s.Bold {
			b.WriteString("\x1b[1m")
		}
		b.WriteString(s.Text)
		b.WriteString("\x1b[0m")
	}
	return b.String()
}

// Plain returns the line's text without styles.
func (l Line) Plain() string {
	var b strings.Builder
	for _, s := range l {
		b.WriteString(s.Text)
	}
	return b.String()
}

// fit pads or cuts the block to exactly w by h.
func (b Block) fit(w, h int) Block {
	out := make(Block, h)
	for i := range out {
		if i < len(b) {
			out[i] = b[i].Fit(w)
		} else {
			out[i] = Line{sp(w)}
		}
	}
	return out
}

// withBG sets a background on every unset segment of every row.
func (b Block) withBG(bg view.Role) Block {
	out := make(Block, len(b))
	for i, l := range b {
		out[i] = l.WithBG(bg)
	}
	return out
}

// col is one column of an hjoin: a block and its width.
type col struct {
	b Block
	w int
}

// hjoin places columns side by side, h rows tall.
func hjoin(h int, cols ...col) Block {
	out := make(Block, h)
	fitted := make([]Block, len(cols))
	for i, c := range cols {
		fitted[i] = c.b.fit(c.w, h)
	}
	for r := range h {
		var l Line
		for _, f := range fitted {
			l = append(l, f[r]...)
		}
		out[r] = l
	}
	return out
}

// vrule is a one-cell-wide vertical rule h rows tall.
func vrule(h int, bd view.Border) col {
	b := make(Block, h)
	for i := range b {
		b[i] = Line{role(view.RoleRule, bd.Vertical)}
	}
	return col{b, 1}
}

// hrule is a horizontal rule w cells wide.
func hrule(w int, bd view.Border) Line {
	return Line{role(view.RoleRule, strings.Repeat(bd.Horizontal, max(0, w)))}
}

// indent shifts every row right by n cells.
func (b Block) indent(n int) Block {
	out := make(Block, len(b))
	for i, l := range b {
		out[i] = append(Line{sp(n)}, l...)
	}
	return out
}

// box draws a border around body, w wide (border included). title, when
// set, sits on the top border.
func box(title Line, body Block, w int, bd view.Border) Block {
	inner := w - 4
	rows := body.fit(inner, len(body))
	top := Line{role(view.RoleRule, bd.TopLeft+bd.Horizontal)}
	if len(title) > 0 {
		t := append(Line{sp(1)}, title...)
		t = append(t, sp(1))
		t = t.Fit(min(t.Width(), inner))
		top = append(top, t...)
		top = append(top, role(view.RoleRule, strings.Repeat(bd.Horizontal, max(0, w-3-t.Width()))+bd.TopRight))
	} else {
		top = append(top, role(view.RoleRule, strings.Repeat(bd.Horizontal, w-3)+bd.TopRight))
	}
	out := Block{top}
	for _, r := range rows {
		l := Line{role(view.RoleRule, bd.Vertical+" ")}
		l = append(l, r...)
		l = append(l, role(view.RoleRule, " "+bd.Vertical))
		out = append(out, l)
	}
	out = append(out, Line{role(view.RoleRule, bd.BottomLeft+strings.Repeat(bd.Horizontal, w-2)+bd.BottomRight)})
	return out
}

// dimmed repaints the block in one faint color, for the backdrop behind a
// dialog.
func (b Block) dimmed() Block {
	out := make(Block, len(b))
	for i, l := range b {
		nl := make(Line, len(l))
		for j, s := range l {
			nl[j] = Seg{Text: s.Text, Role: view.RoleRule, BG: view.RoleCrust}
		}
		out[i] = nl
	}
	return out
}

// overlay draws top over base with its top-left corner at (x, y).
func overlay(base, top Block, x, y int) Block {
	out := append(Block(nil), base...)
	for i, l := range top {
		r := y + i
		if r < 0 || r >= len(out) {
			continue
		}
		row := out[r]
		w := row.Width()
		nl := append(Line(nil), row.Cut(0, x)...)
		nl = append(nl, l...)
		nl = append(nl, row.Cut(x+l.Width(), w)...)
		out[r] = nl
	}
	return out
}

// wrap breaks text into lines at most w cells wide, on spaces. A word
// wider than the line is split by cells, never clipped.
func wrap(text string, w int) []string {
	if w <= 0 {
		return nil
	}
	var out []string
	for _, para := range strings.Split(text, "\n") {
		var words []string
		for _, word := range strings.Fields(para) {
			for width(word) > w {
				head, rest := splitCells(word, w)
				words = append(words, head)
				word = rest
			}
			words = append(words, word)
		}
		cur := ""
		for _, word := range words {
			switch {
			case cur == "":
				cur = word
			case width(cur)+1+width(word) <= w:
				cur += " " + word
			default:
				out = append(out, cur)
				cur = word
			}
		}
		out = append(out, cur)
	}
	return out
}

// bar is a progress bar: done cells full, the rest empty.
func bar(w int, done float64, bd view.Border) Line {
	done = min(max(done, 0), 1)
	d := int(done*float64(w) + 0.5)
	return Line{
		role(view.RoleHealthy, strings.Repeat(bd.Full, d)),
		role(view.RoleRule, strings.Repeat(bd.Empty, max(0, w-d))),
	}
}

// padRight pads s with spaces to w cells, without truncation.
func padRight(s string, w int) string {
	if n := w - width(s); n > 0 {
		return s + strings.Repeat(" ", n)
	}
	return s
}

// joinRight places right at the far end of a w-wide line after left.
func joinRight(left, right Line, w int) Line {
	gap := w - left.Width() - right.Width()
	if gap < 1 {
		return append(left.Fit(max(0, w-right.Width()-1)), append(Line{sp(1)}, right...)...).Fit(w)
	}
	out := append(Line(nil), left...)
	out = append(out, sp(gap))
	return append(out, right...)
}

// splitCells splits s after the most whole graphemes that fit in w cells.
// It always takes at least one grapheme, so a caller looping on it ends.
func splitCells(s string, w int) (head, rest string) {
	n, used, st := 0, 0, -1
	for r := s; r != ""; {
		var g string
		g, r, _, st = uniseg.FirstGraphemeClusterInString(r, st)
		gw := width(g)
		if used+gw > w && n > 0 {
			break
		}
		n += len(g)
		used += gw
	}
	return s[:n], s[n:]
}
