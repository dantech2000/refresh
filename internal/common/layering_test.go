package common

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// common sits under every other layer (awserr, aws, health, monitoring, the
// services all import it), so its non-test files must not import any refresh
// package.
func TestImportsNoRefreshPackage(t *testing.T) {
	const module = "github.com/dantech2000/refresh/"
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		parsed, err := parser.ParseFile(fset, f, src, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range parsed.Imports {
			path, _ := strconv.Unquote(imp.Path.Value)
			if strings.HasPrefix(path, module) {
				t.Errorf("%s imports %s; internal/common must not depend on other refresh packages", f, path)
			}
		}
	}
}
