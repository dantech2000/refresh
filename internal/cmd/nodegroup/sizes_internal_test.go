package nodegroup

import (
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/dantech2000/refresh/internal/apidoc"
	"github.com/dantech2000/refresh/internal/view"
)

// A nodegroup whose sizes EKS did not return shows them as unknown in the
// list cell and in the describe view, never as 0.
func TestUnknownSizes(t *testing.T) {
	n := apidoc.Nodegroup{Name: "web", Status: "ACTIVE", Version: "1.33"}
	if got := nodes(n); got != unknownCell {
		t.Errorf("nodes cell = %q, want %q", got, unknownCell)
	}
	var scaling []view.Field
	for _, s := range describeView(apidoc.NodegroupDescription{Nodegroup: n}).Sections {
		if s.Title == "SCALING" {
			scaling = s.Fields
		}
	}
	want := []view.Field{
		{Name: "desired", Value: unknownCell, Status: view.Unknown},
		{Name: "min", Value: unknownCell, Status: view.Unknown},
		{Name: "max", Value: unknownCell, Status: view.Unknown},
	}
	if diff := cmp.Diff(want, scaling); diff != "" {
		t.Errorf("SCALING (-want +got):\n%s", diff)
	}
}
