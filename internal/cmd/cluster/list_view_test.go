package cluster

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/dantech2000/refresh/internal/apidoc"
	"github.com/dantech2000/refresh/internal/view"
)

var updateViews = flag.Bool("update-views", false, "write the golden files of the table at a terminal width")

// The table at two terminal widths, from the document of the JSON golden
// file. At 100 columns everything fits. At 60 the long name is cut, and
// the columns that must stay whole do.
func TestListView_Widths(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "list.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc apidoc.ClusterList
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	o := &listOptions{}
	for _, tt := range []struct {
		width int
		file  string
	}{
		{100, "list-100.txt"},
		{60, "list-60.txt"},
	} {
		lines := o.view(doc, 3).Render(view.Theme{Unicode: true}, tt.width)
		got := strings.Join(lines, "\n") + "\n"
		for _, line := range lines {
			if w := view.Width(line); w > tt.width {
				t.Errorf("width %d: a line is %d cells wide: %q", tt.width, w, line)
			}
		}
		path := filepath.Join("testdata", tt.file)
		if *updateViews {
			if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		want, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("%v; run the test with -update-views to write it", err)
		}
		if diff := cmp.Diff(string(want), got); diff != "" {
			t.Errorf("%s (-golden +got):\n%s", tt.file, diff)
		}
	}
}
