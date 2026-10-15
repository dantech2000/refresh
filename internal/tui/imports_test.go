package tui_test

import (
	"go/build"
	"strings"
	"testing"
)

// The model draws a state and never calls AWS or Kubernetes: it does not
// import the backend, the AWS clients, the fetcher's packages, or the
// fakes, and nothing of the old tree.
func TestTUIImportsNoBackendAndNoFakes(t *testing.T) {
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
		case rel == "internal/tui/backend", rel == "internal/awsx", rel == "internal/kube", strings.HasPrefix(rel, "internal/cmd/"):
			t.Errorf("tui imports %s; the model draws a state and calls nothing", rel)
		case strings.HasSuffix(rel, "test"):
			t.Errorf("tui imports %s; a fake belongs to tests", rel)
		}
	}
}
