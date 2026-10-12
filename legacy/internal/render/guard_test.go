package render

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// colorImportAllowed lists the non-test files outside internal/render and
// internal/ui that may import github.com/fatih/color. Everything else colors
// through a render.Theme, so color stays additive to a glyph and label and
// every stream decides on color for itself.
var colorImportAllowed = map[string]string{
	"main.go":                            "colors the --help text (section headers, command names) and sets the global --no-color switch",
	"internal/commands/runner/runner.go": "turns fatih's global color off for -o plain; no rendering",
	"internal/mocks/fakeaws/cli.go":      "test harness: redirects and disables fatih's global color for a command run",
}

// statusGlyphs are the status glyphs of the design system and the raw check
// marks it replaced. Outside internal/render (which owns them) and
// internal/ui, a view gets a glyph from a Theme (Token, Line, Glyph,
// Section), so the ASCII fallback applies.
const statusGlyphs = "✓✔✖✗●▲◷○▸"

// TestNoRawColorOrGlyphsOutsideRender fails when a non-test file outside
// internal/render and internal/ui imports fatih/color (unless allowlisted
// above) or writes a status glyph in a string literal.
func TestNoRawColorOrGlyphsOutsideRender(t *testing.T) {
	root := filepath.Join("..", "..")
	fset := token.NewFileSet()
	scanned := 0
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			switch {
			case rel == ".":
				return nil // the module root; its name is ".."
			case rel == "internal/render", rel == "internal/ui", strings.HasPrefix(rel, "internal/ui/"),
				strings.HasPrefix(d.Name(), "."), d.Name() == "testdata", d.Name() == "vendor", d.Name() == "dist":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(rel, ".go") || strings.HasSuffix(rel, "_test.go") {
			return nil
		}
		file, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			return perr
		}
		scanned++
		for _, imp := range file.Imports {
			if p, _ := strconv.Unquote(imp.Path.Value); p == "github.com/fatih/color" {
				if _, ok := colorImportAllowed[rel]; !ok {
					t.Errorf("%s imports github.com/fatih/color; color through a render.Theme (Token, Line, Paint) or add an allowlist entry with a reason", rel)
				}
			}
		}
		ast.Inspect(file, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if ok && lit.Kind == token.STRING && strings.ContainsAny(lit.Value, statusGlyphs) {
				t.Errorf("%s: raw status glyph in %s; use a render token so the ASCII fallback applies", fset.Position(lit.Pos()), lit.Value)
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if scanned < 100 {
		t.Fatalf("scanned %d files; the walk did not reach the module", scanned)
	}
	for f := range colorImportAllowed {
		if _, err := parser.ParseFile(token.NewFileSet(), filepath.Join(root, f), nil, parser.ImportsOnly); err != nil {
			t.Errorf("allowlisted %s: %v (remove the entry if the file is gone)", f, err)
		}
	}
}
