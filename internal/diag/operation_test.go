package diag_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"strings"
	"testing"

	"github.com/dantech2000/refresh/internal/aws/awserr"
)

// TestOperationsAreRequiredPermissions parses operation.go and checks that
// every Op constant names an IAM action in awserr.RequiredPermissions, and
// that the constant's name matches its action.
func TestOperationsAreRequiredPermissions(t *testing.T) {
	listed := map[string]bool{}
	for _, p := range awserr.RequiredPermissions {
		for _, a := range p.Actions {
			listed[a] = true
		}
	}
	file, err := parser.ParseFile(token.NewFileSet(), "operation.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, decl := range file.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.CONST {
			continue
		}
		for _, spec := range gd.Specs {
			vs := spec.(*ast.ValueSpec)
			for i, name := range vs.Names {
				lit, ok := vs.Values[i].(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					t.Errorf("%s: want a string literal", name.Name)
					continue
				}
				action, _ := strconv.Unquote(lit.Value)
				n++
				if !listed[action] {
					t.Errorf("%s = %q is not in awserr.RequiredPermissions", name.Name, action)
				}
				_, api, _ := strings.Cut(action, ":")
				if name.Name != "Op"+api {
					t.Errorf("%s = %q: want the name Op%s", name.Name, action, api)
				}
			}
		}
	}
	if n == 0 {
		t.Fatal("found no Op constants in operation.go")
	}
}
