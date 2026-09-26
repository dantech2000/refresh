package tui

import (
	"image/color"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/rivo/uniseg"
)

// Seg is a run of text with one style. Screens build Lines of Segs and render
// them last, so widths, truncation, and backgrounds are computed on plain
// text and every cell gets an explicit background.
type Seg struct {
	Text string
	FG   color.Color
	BG   color.Color
	Bold bool
}

// Line is one terminal row.
type Line []Seg

// Block is a rectangle of rows.
type Block []Line

// seg helpers keep screen code short.
func tx(s string) Seg                  { return Seg{Text: s, FG: colText} }
func fg(c color.Color, s string) Seg   { return Seg{Text: s, FG: c} }
func bold(c color.Color, s string) Seg { return Seg{Text: s, FG: c, Bold: true} }
func dimS(s string) Seg                { return Seg{Text: s, FG: colDim} }
func sub(s string) Seg                 { return Seg{Text: s, FG: colSubtext} }
func sp(n int) Seg                     { return Seg{Text: strings.Repeat(" ", max(0, n))} }

// chip is a key hint: the key on a raised background.
func chip(k string) Seg { return Seg{Text: " " + k + " ", FG: colText, BG: colSurface0} }

// badge is a label on a solid color.
func badge(c color.Color, s string) Seg {
	return Seg{Text: " " + s + " ", FG: colCrust, BG: c, Bold: true}
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

// Fit truncates the line to w cells (ending in "…" when cut) or pads it
// with spaces to exactly w.
func (l Line) Fit(w int) Line {
	if w <= 0 {
		return nil
	}
	if lw := l.Width(); lw <= w {
		if lw == w {
			return l
		}
		return append(append(Line(nil), l...), sp(w-lw))
	}
	out := l.Cut(0, w-1)
	last := Seg{Text: "…", FG: colDim}
	if len(out) > 0 {
		last.BG = out[len(out)-1].BG
	}
	return append(out, last).Fit(w)
}

// Cut returns the cells [from, to) of the line. It splits text only between
// graphemes (a user-perceived character such as 👩‍💻), so it never breaks one
// apart; a wide grapheme that straddles an edge becomes spaces.
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
func (l Line) WithBG(bg color.Color) Line {
	out := make(Line, len(l))
	for i, s := range l {
		if s.BG == nil {
			s.BG = bg
		}
		out[i] = s
	}
	return out
}

// Render styles the line; segments with no background get bg.
func (l Line) Render(bg color.Color) string {
	var b strings.Builder
	for _, s := range l {
		if s.Text == "" {
			continue
		}
		st := lipgloss.NewStyle().Foreground(colText).Background(bg)
		if s.FG != nil {
			st = st.Foreground(s.FG)
		}
		if s.BG != nil {
			st = st.Background(s.BG)
		}
		if s.Bold {
			st = st.Bold(true)
		}
		b.WriteString(st.Render(s.Text))
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

// fit pads or cuts the block to exactly w×h.
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
func (b Block) withBG(bg color.Color) Block {
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
func vrule(h int) col {
	b := make(Block, h)
	for i := range b {
		b[i] = Line{fg(colSurface1, "│")}
	}
	return col{b, 1}
}

// hrule is a horizontal rule w cells wide.
func hrule(w int) Line { return Line{fg(colSurface1, strings.Repeat("─", max(0, w)))} }

// indent shifts every row right by n cells.
func (b Block) indent(n int) Block {
	out := make(Block, len(b))
	for i, l := range b {
		out[i] = append(Line{sp(n)}, l...)
	}
	return out
}

// box draws a rounded border around body, w wide (border included). title,
// when set, sits on the top border.
func box(title Line, body Block, w int, border color.Color) Block {
	inner := w - 4
	rows := body.fit(inner, len(body))
	top := Line{fg(border, "╭─")}
	if len(title) > 0 {
		t := append(Line{sp(1)}, title...)
		t = append(t, sp(1))
		t = t.Fit(min(t.Width(), inner))
		top = append(top, t...)
		top = append(top, fg(border, strings.Repeat("─", max(0, w-3-t.Width()))+"╮"))
	} else {
		top = append(top, fg(border, strings.Repeat("─", w-3)+"╮"))
	}
	out := Block{top}
	for _, r := range rows {
		l := Line{fg(border, "│ ")}
		l = append(l, r...)
		l = append(l, fg(border, " │"))
		out = append(out, l)
	}
	out = append(out, Line{fg(border, "╰"+strings.Repeat("─", w-2)+"╯")})
	return out
}

// dimmed repaints the block in one faint color, for the backdrop behind a
// dialog.
func (b Block) dimmed() Block {
	out := make(Block, len(b))
	for i, l := range b {
		nl := make(Line, len(l))
		for j, s := range l {
			nl[j] = Seg{Text: s.Text, FG: colSurface1, BG: colCrust}
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

// wrap breaks text into lines at most w cells wide, on spaces.
func wrap(text string, w int) []string {
	if w <= 0 {
		return nil
	}
	var out []string
	for _, para := range strings.Split(text, "\n") {
		var words []string
		for _, word := range strings.Fields(para) {
			// A word wider than the line is split by cells, never clipped.
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

// bar is a block-glyph progress bar: done in green, active in teal, the
// rest faint.
func bar(w int, done, active float64) Line {
	done = clamp01(done)
	active = clamp01(active)
	d := int(done*float64(w) + 0.5)
	a := int((done+active)*float64(w)+0.5) - d
	if d+a > w {
		a = w - d
	}
	return Line{
		fg(colGreen, strings.Repeat("█", d)),
		fg(colTeal, strings.Repeat("█", max(0, a))),
		fg(colSurface1, strings.Repeat("░", max(0, w-d-a))),
	}
}

func clamp01(f float64) float64 {
	switch {
	case f < 0:
		return 0
	case f > 1:
		return 1
	}
	return f
}

// padRight pads s with spaces to w cells (no truncation).
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
