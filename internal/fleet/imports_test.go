package fleet_test

import (
	"go/build"
	"slices"
	"strings"
	"testing"
)

// fleet is above apidoc and awsx, and below health and change. It reads
// no flag and writes to no terminal, so it imports nothing of the command
// layer: no internal/cmd package, cmdutil, view, contexts, or build.
func TestFleetImports(t *testing.T) {
	const module = "github.com/dantech2000/refresh/"
	// allowed are the refresh packages that the package may import.
	allowed := []string{"internal/apidoc", "internal/awsx", "internal/fanout", "internal/fleet/fleetdata"}
	// testOnly are the ones that only its tests may import.
	testOnly := []string{"internal/awsx/awsxtest", "internal/fleet", "internal/fleet/fleettest"}
	pkg, err := build.ImportDir(".", 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, imp := range pkg.Imports {
		if rel, ok := strings.CutPrefix(imp, module); ok && !slices.Contains(allowed, rel) {
			t.Errorf("fleet imports %s; it may import only %v", rel, allowed)
		}
	}
	for _, imp := range slices.Concat(pkg.TestImports, pkg.XTestImports) {
		if rel, ok := strings.CutPrefix(imp, module); ok && !slices.Contains(allowed, rel) && !slices.Contains(testOnly, rel) {
			t.Errorf("a test of fleet imports %s; the tests may import only %v and %v", rel, allowed, testOnly)
		}
	}
}
