package backend_test

import (
	"go/build"
	"strings"
	"testing"
)

// The backend reads the fleet for the model and knows nothing of a
// terminal: it does not import the model, the command packages, the
// fakes, or the old tree.
func TestBackendImportsNoModelAndNoFakes(t *testing.T) {
	pkg, err := build.ImportDir(".", 0)
	if err != nil {
		t.Fatal(err)
	}
	const module = "github.com/dantech2000/refresh/"
	for _, imp := range pkg.Imports {
		rel, ok := strings.CutPrefix(imp, module)
		if !ok {
			continue
		}
		switch {
		case strings.HasPrefix(rel, "legacy/"):
			t.Errorf("backend imports %s; the new tree does not import the old one", rel)
		case rel == "internal/tui", strings.HasPrefix(rel, "internal/cmd/"):
			t.Errorf("backend imports %s; nothing of the terminal belongs here", rel)
		case strings.HasSuffix(rel, "test"):
			t.Errorf("backend imports %s; a fake belongs to tests", rel)
		}
	}
	if strings.Contains(strings.Join(pkg.Imports, " "), "charm.land") {
		t.Error("backend imports Bubble Tea or Lip Gloss; it draws nothing")
	}
}
