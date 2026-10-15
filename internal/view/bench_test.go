package view_test

import (
	"fmt"
	"testing"

	"github.com/dantech2000/refresh/internal/view"
)

// benchList returns a list of n rows, as "refresh status" has for a fleet.
func benchList(n int) view.List[row] {
	rows := make([]row, n)
	for i := range rows {
		rows[i] = row{name: fmt.Sprintf("cluster-%03d", i), status: "ACTIVE", note: "a note for the row", count: 1 + i%3}
	}
	l := newList(rows...)
	return l
}

func benchDescription() view.Description {
	d := newDescription()
	for i := range 30 {
		d.Sections[2].Items = append(d.Sections[2].Items, view.Item{Name: fmt.Sprintf("i-%02d", i), Fields: []view.Field{
			{Name: "state", Value: "running", Status: view.Healthy}, {Name: "zone", Value: "us-east-1a"},
		}})
	}
	return d
}

func BenchmarkListRender(b *testing.B) {
	l := benchList(200)
	th := view.Theme{Depth: view.TrueColor, Unicode: true}
	b.ReportAllocs()
	for b.Loop() {
		l.Render(th, 120)
	}
}

func BenchmarkDescriptionRender(b *testing.B) {
	d := benchDescription()
	th := view.Theme{Depth: view.TrueColor, Unicode: true}
	b.ReportAllocs()
	for b.Loop() {
		d.Render(th, 120)
	}
}

// The gates below bound the allocations of the views. They are not tight:
// they catch a change that makes a view allocate for each cell where it
// allocated for each row, which a table of a large fleet would show.

func TestListRenderAllocations(t *testing.T) {
	const rows = 200
	l := benchList(rows)
	th := view.Theme{Depth: view.TrueColor, Unicode: true}
	got := testing.AllocsPerRun(20, func() { l.Render(th, 120) })
	// Four columns: the text and the glyph of each cell, its padded form,
	// and the line.
	if limit := float64(rows * 4 * 6); got > limit {
		t.Errorf("List.Render of %d rows made %.0f allocations, want at most %.0f", rows, got, limit)
	}
	t.Logf("List.Render of %d rows: %.0f allocations, %.1f for a row", rows, got, got/rows)
}

func TestDescriptionRenderAllocations(t *testing.T) {
	d := benchDescription()
	th := view.Theme{Depth: view.TrueColor, Unicode: true}
	got := testing.AllocsPerRun(20, func() { d.Render(th, 120) })
	if limit := 1200.0; got > limit {
		t.Errorf("Description.Render made %.0f allocations, want at most %.0f", got, limit)
	}
	t.Logf("Description.Render with 32 items: %.0f allocations", got)
}
