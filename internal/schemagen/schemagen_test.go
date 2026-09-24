package schemagen_test

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/dantech2000/refresh/internal/apidoc"
	"github.com/dantech2000/refresh/internal/commands/documents"
	"github.com/dantech2000/refresh/internal/diag"
	"github.com/dantech2000/refresh/internal/schemagen"
)

const modulePath = "github.com/dantech2000/refresh"

type color string

func (color) EnumValues() []string { return []string{"Red", "Blue"} }

type item struct {
	Name  string `json:"name"`
	Color color  `json:"color,omitempty"`
}

type sampleDoc struct {
	Items    []item            `json:"items"`
	Tags     map[string]string `json:"tags"`
	Note     *string           `json:"note,omitempty"`
	Hidden   string            `json:"-"`
	Failures diag.List         `json:"failures"`
}

func (sampleDoc) DocumentKind() apidoc.Kind { return "Sample" }

func generate(t *testing.T, doc apidoc.Document) map[string]any {
	t.Helper()
	data, err := schemagen.Generate(doc, nil)
	if err != nil {
		t.Fatal(err)
	}
	var s map[string]any
	if err := json.Unmarshal(data, &s); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestGenerate(t *testing.T) {
	data, err := schemagen.Generate(sampleDoc{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	// apiVersion and kind are the first two properties of the document
	// (the last "properties" key; the $defs come first).
	props := string(data[strings.LastIndex(string(data), `"properties"`):])
	if a, k, i := strings.Index(props, `"apiVersion"`), strings.Index(props, `"kind"`), strings.Index(props, `"items"`); a >= k || k >= i {
		t.Errorf("apiVersion and kind are not the first properties:\n%s", data)
	}

	s := generate(t, sampleDoc{})
	if s["$id"] != apidoc.SchemaBaseURL+"Sample.json" || s["$schema"] != "https://json-schema.org/draft/2020-12/schema" {
		t.Errorf("$id, $schema = %v, %v", s["$id"], s["$schema"])
	}
	if got, want := s["required"], []any{"apiVersion", "kind", "items", "tags", "failures"}; !reflect.DeepEqual(got, want) {
		t.Errorf("required = %v, want %v (omitempty fields are optional)", got, want)
	}
	p := s["properties"].(map[string]any)
	if p["kind"].(map[string]any)["const"] != "Sample" || p["apiVersion"].(map[string]any)["const"] != apidoc.APIVersion {
		t.Errorf("apiVersion/kind are not constants: %v %v", p["apiVersion"], p["kind"])
	}
	if _, ok := p["Hidden"]; ok {
		t.Error(`a json:"-" field is in the schema`)
	}
	// A slice or map without omitempty can be null; diag.List cannot.
	for _, name := range []string{"items", "tags"} {
		if _, ok := p[name].(map[string]any)["anyOf"]; !ok {
			t.Errorf("%s does not allow null: %v", name, p[name])
		}
	}
	if _, ok := p["failures"].(map[string]any)["anyOf"]; ok {
		t.Errorf("failures allows null: %v", p["failures"])
	}
	if _, ok := p["note"].(map[string]any)["anyOf"]; ok {
		t.Errorf("an omitempty pointer allows null: %v", p["note"])
	}
	defs := s["$defs"].(map[string]any)
	if got := defs["Color"].(map[string]any)["enum"]; !reflect.DeepEqual(got, []any{"Red", "Blue"}) {
		t.Errorf("Color enum = %v", got)
	}
	if _, ok := defs["FailureReason"].(map[string]any)["enum"]; !ok {
		t.Errorf("FailureReason is not an enum: %v", defs["FailureReason"])
	}
	if _, ok := s["additionalProperties"]; ok {
		t.Error("the schema rejects unknown properties; v1 consumers must accept them")
	}
}

// Item has the schema name of item ("Item" once capitalized).
type Item struct {
	X int `json:"x"`
}

type collidingDoc struct {
	A item `json:"a"`
	B Item `json:"b"`
}

func (collidingDoc) DocumentKind() apidoc.Kind { return "Colliding" }

func TestGenerate_RejectsNameCollision(t *testing.T) {
	if _, err := schemagen.Generate(collidingDoc{}, nil); err == nil {
		t.Error("two types with the schema name Item got one schema")
	}
}

type headerDoc struct {
	Kind string `json:"kind"`
}

func (headerDoc) DocumentKind() apidoc.Kind { return "Header" }

func TestGenerate_RejectsOwnKindField(t *testing.T) {
	if _, err := schemagen.Generate(headerDoc{}, nil); err == nil {
		t.Error("a document with its own kind field got a schema")
	}
}

// Every named string type in a document is an enum (apidoc.Enum), so the
// schema lists its values, and EnumValues lists every constant of the type.
func TestEnumsAreComplete(t *testing.T) {
	abs, err := filepath.Abs(".")
	if err != nil {
		t.Fatal(err)
	}
	root, err := schemagen.FindModuleRoot(abs, modulePath)
	if err != nil {
		t.Fatal(err)
	}
	consts := stringConsts(t, root)

	enumType := reflect.TypeFor[apidoc.Enum]()
	seen := map[reflect.Type]bool{}
	var walk func(reflect.Type)
	walk = func(t2 reflect.Type) {
		if seen[t2] {
			return
		}
		seen[t2] = true
		switch t2.Kind() {
		case reflect.Pointer, reflect.Slice, reflect.Map:
			walk(t2.Elem())
		case reflect.Struct:
			if t2 == reflect.TypeFor[time.Time]() {
				return
			}
			for i := range t2.NumField() {
				walk(t2.Field(i).Type)
			}
		case reflect.String:
			if t2.Name() == "string" || t2.PkgPath() == "" {
				return
			}
			if !t2.Implements(enumType) {
				t.Errorf("%s is a named string in a document but not an apidoc.Enum; add EnumValues", t2)
				return
			}
			values := reflect.Zero(t2).Interface().(apidoc.Enum).EnumValues()
			for _, c := range consts[t2.PkgPath()+"."+t2.Name()] {
				if !slices.Contains(values, c) {
					t.Errorf("%s.EnumValues() lacks the constant %q", t2, c)
				}
			}
		}
	}
	for _, doc := range documents.All() {
		walk(reflect.TypeOf(doc))
	}
}

// stringConsts maps "importpath.Type" to the string values of the typed
// constants declared in the module's non-test Go files.
func stringConsts(t *testing.T, root string) map[string][]string {
	t.Helper()
	out := map[string][]string{}
	fset := token.NewFileSet()
	err := filepath.WalkDir(filepath.Join(root, "internal"), func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return err
		}
		f, err := parser.ParseFile(fset, p, nil, 0)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, filepath.Dir(p))
		pkg := path.Join(modulePath, filepath.ToSlash(rel))
		for _, decl := range f.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.CONST {
				continue
			}
			for _, spec := range gd.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok || vs.Type == nil {
					continue
				}
				typ, ok := vs.Type.(*ast.Ident)
				if !ok {
					continue
				}
				for _, v := range vs.Values {
					lit, ok := v.(*ast.BasicLit)
					if !ok || lit.Kind != token.STRING {
						continue
					}
					s, err := strconv.Unquote(lit.Value)
					if err == nil {
						out[pkg+"."+typ.Name] = append(out[pkg+"."+typ.Name], s)
					}
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}
