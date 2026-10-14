package view

import (
	"cmp"
	"io"
	"strconv"
	"strings"

	"github.com/dantech2000/refresh/internal/apidoc"
)

// Description is the view of a describe command: one thing, in sections.
// The table and plain output come from the same sections, so they show the
// same fields.
type Description struct {
	// Title is the name of the thing.
	Title string
	// Status and Label stand beside the title: the glyph and its text.
	Status Status
	Label  string
	// Subtitle is dim text after the label, such as the cluster of an
	// add-on.
	Subtitle string
	Sections []Section
	// Failures are listed in a section at the end of the table. Plain
	// output does not have them: the command writes them to stderr.
	Failures []apidoc.Failure
	// FailuresTitle is the heading of that section. Empty means
	// INCOMPLETE DATA, the heading of a read.
	FailuresTitle string
}

// Section is one part of a description. A section with no field and no
// item is left out of both forms.
type Section struct {
	// Title is the heading, in capitals: "OVERVIEW".
	Title string
	// Fields are rows of a name and a value.
	Fields []Field
	// Items are things that repeat, such as one for each nodegroup. Every
	// item has the same fields, which become the columns of a table.
	Items []Item
}

// Field is a name and a value. A field with an empty value is left out.
type Field struct {
	Name, Value string
	// Status, when it is set, puts its glyph before the value in the
	// table.
	Status Status
}

// Item is one of the things that repeat in a section.
type Item struct {
	Name   string
	Fields []Field
	// Key names the item in the plain view when Name is not unique in
	// its section. "" means Name.
	Key string
}

// Render returns the lines of the table view. width is the width of the
// terminal, or 0 when the output is not one.
func (d Description) Render(th Theme, width int) []string {
	head := th.Strong(d.Title)
	if d.Label != "" {
		head += "   " + th.Token(d.Status, d.Label)
	}
	if d.Subtitle != "" {
		head += "   " + th.Dim(d.Subtitle)
	}
	out := []string{head}
	for _, s := range d.Sections {
		fields := present(s.Fields)
		if len(fields) == 0 && len(s.Items) == 0 {
			continue
		}
		heading := th.Heading(s.Title)
		if len(s.Items) > 0 {
			heading += "  " + th.Dim(strconv.Itoa(len(s.Items)))
		}
		out = append(out, "", heading)
		out = append(out, fieldLines(th, fields)...)
		for _, line := range itemTable(s.Items).table(th, max(width-len(indent), 0)) {
			out = append(out, indent+line)
		}
	}
	return append(out, failureSection(th, d.FailuresTitle, d.Failures)...)
}

// indent stands before every line of a section.
const indent = "  "

// present returns the fields that have a value.
func present(fields []Field) []Field {
	var out []Field
	for _, f := range fields {
		if strings.TrimSpace(f.Value) != "" {
			out = append(out, f)
		}
	}
	return out
}

// fieldLines returns the fields as lines of a name and a value, with the
// values in one column. A value of several lines keeps its lines.
func fieldLines(th Theme, fields []Field) []string {
	nameWidth := 0
	for _, f := range fields {
		nameWidth = max(nameWidth, Width(f.Name))
	}
	var out []string
	for _, f := range fields {
		lines := strings.Split(strings.TrimRight(f.Value, "\n"), "\n")
		first := lines[0]
		if f.Status != NoStatus {
			first = th.Token(f.Status, first)
		}
		out = append(out, indent+th.Dim(pad(f.Name, nameWidth, false))+columnGap+first)
		for _, line := range lines[1:] {
			out = append(out, indent+strings.Repeat(" ", nameWidth)+columnGap+line)
		}
	}
	return out
}

// itemTable returns the items as a list: a NAME column, then one column for
// each field of the first item.
func itemTable(items []Item) List[Item] {
	if len(items) == 0 {
		return List[Item]{}
	}
	l := List[Item]{Rows: items, Columns: []Column[Item]{{Title: "NAME", Value: func(it Item) string { return it.Name }}}}
	for i, f := range items[0].Fields {
		l.Columns = append(l.Columns, Column[Item]{
			Title: strings.ToUpper(f.Name),
			Value: func(it Item) string {
				if i < len(it.Fields) {
					return it.Fields[i].Value
				}
				return ""
			},
			Status: itemStatus(items, i),
		})
	}
	return l
}

// itemStatus returns the status function for field i of the items, or nil
// when no item has a status there: a column without glyphs.
func itemStatus(items []Item, i int) func(Item) Status {
	for _, it := range items {
		if i < len(it.Fields) && it.Fields[i].Status != NoStatus {
			return func(it Item) Status {
				if i < len(it.Fields) {
					return it.Fields[i].Status
				}
				return NoStatus
			}
		}
	}
	return nil
}

// Plain writes the description as tab-separated FIELD and VALUE rows. The
// name of a field is its section, the item when there is one, and the
// field, in lower case and joined by dots:
//
//	overview.status	ACTIVE
//	nodegroups.web.desired	3
func (d Description) Plain(w io.Writer) error {
	var b strings.Builder
	b.WriteString("FIELD\tVALUE\n")
	row := func(value string, key ...string) {
		b.WriteString(strings.Join(key, ".") + "\t" + plainCell(value) + "\n")
	}
	for _, s := range d.Sections {
		section := plainKey(s.Title)
		for _, f := range present(s.Fields) {
			row(f.Value, section, plainKey(f.Name))
		}
		for _, it := range s.Items {
			for _, f := range it.Fields {
				// An item name is an AWS name: it stays as it is.
				row(f.Value, section, plainCell(cmp.Or(it.Key, it.Name)), plainKey(f.Name))
			}
		}
	}
	_, err := io.WriteString(w, b.String())
	return err
}

// plainKey returns a title or a field name as a part of a plain FIELD: in
// lower case, with an underscore for each space.
func plainKey(s string) string {
	return strings.ReplaceAll(strings.ToLower(strings.TrimSpace(s)), " ", "_")
}
