package change

import (
	"go/build"
	"slices"
	"strings"
	"testing"
)

// change is above fleet and health, and below the command layer. It
// reads no flag and writes to no terminal, so it imports nothing of that
// layer: no internal/cmd package, cmdutil, view, contexts, or build. The
// exit codes of a plan and a run are in internal/exitcode, and the front
// end of a run is in internal/cmd/changecmd.
func TestChangeImports(t *testing.T) {
	const module = "github.com/dantech2000/refresh/"
	// allowed are the refresh packages that the package may import.
	allowed := []string{"internal/apidoc", "internal/awsx", "internal/exitcode", "internal/fleet", "internal/health", "internal/kube"}
	// testOnly are the ones that only its tests may import.
	testOnly := []string{"internal/awsx/awsxtest", "internal/fleet/fleettest", "internal/kube/kubetest"}
	pkg, err := build.ImportDir(".", 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, imp := range pkg.Imports {
		if rel, ok := strings.CutPrefix(imp, module); ok && !slices.Contains(allowed, rel) {
			t.Errorf("change imports %s; it may import only %v", rel, allowed)
		}
	}
	for _, imp := range slices.Concat(pkg.TestImports, pkg.XTestImports) {
		if rel, ok := strings.CutPrefix(imp, module); ok && !slices.Contains(allowed, rel) && !slices.Contains(testOnly, rel) {
			t.Errorf("a test of change imports %s; the tests may import only %v and %v", rel, allowed, testOnly)
		}
	}
}
