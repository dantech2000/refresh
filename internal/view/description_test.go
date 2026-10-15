package view_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/dantech2000/refresh/internal/apidoc"
	"github.com/dantech2000/refresh/internal/view"
)

func newDescription() view.Description {
	return view.Description{
		Title:    "web",
		Status:   view.Healthy,
		Label:    "ACTIVE",
		Subtitle: "prod",
		Sections: []view.Section{
			{Title: "OVERVIEW", Fields: []view.Field{
				{Name: "status", Value: "ACTIVE", Status: view.Healthy},
				{Name: "ami type", Value: "AL2023_x86_64_STANDARD"},
				{Name: "not set", Value: ""},
				{Name: "notes", Value: "first line\nsecond line\n"},
			}},
			{Title: "EMPTY", Fields: []view.Field{{Name: "nothing", Value: " "}}},
			{Title: "INSTANCES", Items: []view.Item{
				{Name: "i-1", Fields: []view.Field{{Name: "state", Value: "running", Status: view.Healthy}, {Name: "zone", Value: "us-east-1a"}}},
				{Name: "i-2", Fields: []view.Field{{Name: "state", Value: "pending", Status: view.Warn}, {Name: "zone", Value: ""}}},
			}},
		},
		Failures: []apidoc.Failure{{Kind: apidoc.FailureNodegroup, Name: "web", Reason: apidoc.ReasonThrottled}},
	}
}

func TestDescription_Render(t *testing.T) {
	want := []string{
		"web   [OK] ACTIVE   prod",
		"",
		"OVERVIEW",
		"  status    [OK] ACTIVE",
		"  ami type  AL2023_x86_64_STANDARD",
		"  notes     first line",
		"            second line",
		"",
		"INSTANCES  2",
		"  NAME  STATE         ZONE",
		"  i-1   [OK] running  us-east-1a",
		"  i-2   [!] pending   -",
		"",
		"INCOMPLETE DATA",
		"[?] nodegroup web: Throttled",
	}
	if diff := cmp.Diff(want, newDescription().Render(view.Theme{}, 0)); diff != "" {
		t.Errorf("Render (-want +got):\n%s", diff)
	}
}

// Plain output has the fields of the table: the same sections, the same
// names, the same values, one row each.
func TestDescription_Plain(t *testing.T) {
	var buf bytes.Buffer
	if err := newDescription().Plain(&buf); err != nil {
		t.Fatal(err)
	}
	want := strings.Join([]string{
		"FIELD\tVALUE",
		"overview.status\tACTIVE",
		"overview.ami_type\tAL2023_x86_64_STANDARD",
		"overview.notes\tfirst line second line",
		"instances.i-1.state\trunning",
		"instances.i-1.zone\tus-east-1a",
		"instances.i-2.state\tpending",
		"instances.i-2.zone\t-",
		"",
	}, "\n")
	if diff := cmp.Diff(want, buf.String()); diff != "" {
		t.Errorf("Plain (-want +got):\n%s", diff)
	}
}

// The item table of a section is cut to the terminal like any list.
func TestDescription_NarrowTerminal(t *testing.T) {
	d := view.Description{Title: "x", Sections: []view.Section{{Title: "ISSUES", Items: []view.Item{
		{Name: "Code", Fields: []view.Field{{Name: "message", Value: strings.Repeat("long ", 30)}}},
	}}}}
	for _, line := range d.Render(view.Theme{Unicode: true}, 40) {
		if w := view.Width(line); w > 40 {
			t.Errorf("a line is %d cells wide on a terminal of 40: %q", w, line)
		}
	}
}
