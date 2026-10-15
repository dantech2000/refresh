package health_test

import (
	"go/build"
	"slices"
	"strings"
	"testing"
)

// health is above fleet and below the command layer. It reads no flag
// and writes to no terminal, so it imports nothing of that layer: no
// internal/cmd package, cmdutil, view, contexts, or build. The exit codes
// that a gate and a verification return are in internal/exitcode.
func TestHealthImports(t *testing.T) {
	const module = "github.com/dantech2000/refresh/"
	// allowed are the refresh packages that the package may import.
	allowed := []string{"internal/apidoc", "internal/awsx", "internal/exitcode", "internal/fanout", "internal/fleet"}
	// testOnly are the ones that only its tests may import.
	testOnly := []string{"internal/awsx/awsxtest", "internal/fleet/fleettest", "internal/health", "internal/kube/kubetest"}
	pkg, err := build.ImportDir(".", 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, imp := range pkg.Imports {
		if rel, ok := strings.CutPrefix(imp, module); ok && !slices.Contains(allowed, rel) {
			t.Errorf("health imports %s; it may import only %v", rel, allowed)
		}
	}
	for _, imp := range slices.Concat(pkg.TestImports, pkg.XTestImports) {
		if rel, ok := strings.CutPrefix(imp, module); ok && !slices.Contains(allowed, rel) && !slices.Contains(testOnly, rel) {
			t.Errorf("a test of health imports %s; the tests may import only %v and %v", rel, allowed, testOnly)
		}
	}
}
