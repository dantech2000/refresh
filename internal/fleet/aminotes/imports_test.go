package aminotes_test

import (
	"go/build"
	"slices"
	"strings"
	"testing"
)

// aminotes fetches and parses release notes, and returns lines for a
// command to print. It imports no other refresh package: it reads no flag,
// writes to no terminal, and needs nothing of AWS or the fleet.
func TestAminotesImportsNoRefreshPackage(t *testing.T) {
	const module = "github.com/dantech2000/refresh/"
	pkg, err := build.ImportDir(".", 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, imp := range slices.Concat(pkg.Imports, pkg.TestImports, pkg.XTestImports) {
		if rel, ok := strings.CutPrefix(imp, module); ok {
			t.Errorf("aminotes or its tests import %s; it imports no refresh package", rel)
		}
	}
}
