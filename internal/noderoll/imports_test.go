package noderoll

import (
	"go/build"
	"slices"
	"strings"
	"testing"
)

// noderoll reads Kubernetes with client-go and knows nothing of refresh:
// no fleet, no change, no view. Whoever runs a roll gives it a clientset
// and the name of a nodegroup. Only its tests import refresh packages, for
// the fake world and the fake API server.
func TestNoderollImports(t *testing.T) {
	const module = "github.com/dantech2000/refresh/"
	// testOnly are the refresh packages that only the tests may import.
	testOnly := []string{"internal/fleet/fleettest", "internal/kube/kubetest"}
	pkg, err := build.ImportDir(".", 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, imp := range pkg.Imports {
		if rel, ok := strings.CutPrefix(imp, module); ok {
			t.Errorf("noderoll imports %s; it may import no package of refresh", rel)
		}
	}
	for _, imp := range slices.Concat(pkg.TestImports, pkg.XTestImports) {
		if rel, ok := strings.CutPrefix(imp, module); ok && !slices.Contains(testOnly, rel) {
			t.Errorf("a test of noderoll imports %s; the tests may import only %v", rel, testOnly)
		}
	}
}
