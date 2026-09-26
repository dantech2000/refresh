package render

import "strings"

// ErrorLines styles a top-level error for the terminal. The first line is
// the headline (the fail mark and "Error:", red and bold). The rest is help
// text, styled by role: the cause ("AWS: …" or "Cause: …") stands out, a
// heading (a line ending in ":" or "):") is bold, an indented two-column row
// dims its second column (and marks a row the AWS line names, such as the
// denied action), and a "See <url>" line is a link. Everything else stays
// plain, so long help does not read as one red block.
//
// width is the terminal's column count; a line wider than it wraps at a word
// with a hanging indent. width <= 0 (not a terminal) leaves lines unwrapped.
func (t *Theme) ErrorLines(msg string, width int) []string {
	lines := strings.Split(strings.TrimRight(msg, "\n"), "\n")
	aws := "" // what AWS said, to mark the rows it names
	for _, l := range lines {
		if s := strings.TrimSpace(l); strings.HasPrefix(s, "AWS: ") || strings.HasPrefix(s, "Cause: ") {
			aws = s
		}
	}
	var out []string
	// emit wraps text after a lead of lead columns (already styled in
	// head), painting each wrapped piece with paint.
	emit := func(head string, lead int, text string, paint func(string) string) {
		for i, piece := range wrapWords(text, width-lead) {
			if i == 0 {
				out = append(out, head+paint(piece))
			} else {
				out = append(out, strings.Repeat(" ", lead)+paint(piece))
			}
		}
	}
	plain := func(s string) string { return t.Paint(t.Pal.Text, s) }
	dim := func(s string) string { return t.Paint(t.Pal.Dim, s) }

	mark := t.Mark(Fail) + " "
	emit(mark, 2, "Error: "+lines[0], func(s string) string { return t.Bold(t.Pal.Red, s) })
	for _, l := range lines[1:] {
		s := strings.TrimSpace(l)
		switch {
		case s == "":
			out = append(out, "")
		case strings.HasPrefix(s, "AWS: "), strings.HasPrefix(s, "Cause: "):
			label, text, _ := strings.Cut(s, ": ")
			emit("  "+dim(label)+" ", 3+len(label), text, func(p string) string { return t.Paint(t.Pal.Yellow, p) })
		case strings.HasPrefix(s, "See http"):
			out = append(out, "  See "+t.Paint(t.Pal.Sky, strings.TrimPrefix(s, "See ")))
		case strings.HasSuffix(s, ":") || strings.HasSuffix(s, "):"):
			emit("  ", 2, s, func(p string) string { return t.Bold(t.Pal.Text, p) })
		case strings.HasPrefix(l, "  "):
			// An indented row: first column plain, the rest dim.
			body := strings.TrimPrefix(l, "  ")
			first, rest := body, ""
			if i := strings.Index(body, "  "); i > 0 {
				first, rest = body[:i], strings.TrimLeft(body[i:], " ")
			}
			col := len(body) - len(rest) // the second column's offset
			if rest == "" {
				col = len(body)
			}
			if aws != "" && strings.Contains(aws, first+" ") {
				// The row the AWS line names, such as the denied action.
				head := "  " + t.Paint(t.Pal.Yellow, t.Mark(Warn)+" "+first) + strings.Repeat(" ", col-len(first))
				emit(head, 4+col, rest, func(p string) string { return t.Paint(t.Pal.Subtext, p) })
				continue
			}
			emit("    "+plain(first)+strings.Repeat(" ", col-len(first)), 4+col, rest, dim)
		default:
			emit("  ", 2, s, plain)
		}
	}
	return out
}

// wrapWords splits s into lines of at most width display columns, breaking
// at spaces; a word longer than width is split. width <= 0 means no limit.
func wrapWords(s string, width int) []string {
	if width <= 0 || len(s) <= width {
		return []string{s}
	}
	var out []string
	line := ""
	for _, w := range strings.Fields(s) {
		for len(w) > width {
			if line != "" {
				out = append(out, line)
				line = ""
			}
			out = append(out, w[:width])
			w = w[width:]
		}
		switch {
		case line == "":
			line = w
		case len(line)+1+len(w) <= width:
			line += " " + w
		default:
			out = append(out, line)
			line = w
		}
	}
	if line != "" {
		out = append(out, line)
	}
	return out
}
