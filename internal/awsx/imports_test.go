package awsx_test

import (
	"go/build"
	"slices"
	"strings"
	"testing"
)

// awsx is at the bottom: it has the clients, the retry, and the errors,
// and imports no other refresh package. Its tests use the fake server.
func TestAwsxImports(t *testing.T) {
	const module = "github.com/dantech2000/refresh/"
	// allowed are the refresh packages that the package may import.
	allowed := []string{}
	// testOnly are the ones that only its tests may import.
	testOnly := []string{"internal/awsx", "internal/awsx/awsxtest", "internal/fleet/fleettest"}
	pkg, err := build.ImportDir(".", 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, imp := range pkg.Imports {
		if rel, ok := strings.CutPrefix(imp, module); ok && !slices.Contains(allowed, rel) {
			t.Errorf("awsx imports %s; it may import only %v", rel, allowed)
		}
	}
	for _, imp := range slices.Concat(pkg.TestImports, pkg.XTestImports) {
		if rel, ok := strings.CutPrefix(imp, module); ok && !slices.Contains(allowed, rel) && !slices.Contains(testOnly, rel) {
			t.Errorf("a test of awsx imports %s; the tests may import only %v and %v", rel, allowed, testOnly)
		}
	}
}
