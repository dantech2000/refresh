package schemagen

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"unicode"
)

// Comments holds the Go doc comments of the module's types and fields, for
// the schema descriptions.
type Comments struct {
	// byKey maps "importpath.Type" and "importpath.Type.Field" to the
	// comment text.
	byKey map[string]string
	// fields maps "importpath.Type.Field" to the field's JSON name, so a
	// description that starts with the Go field name can use the JSON name.
	fields map[string]string
}

// LoadComments parses the Go files under dir/internal, the source of the
// module modulePath rooted at dir. Test files are skipped.
func LoadComments(dir, modulePath string) (*Comments, error) {
	c := &Comments{byKey: map[string]string{}, fields: map[string]string{}}
	fset := token.NewFileSet()
	root := filepath.Join(dir, "internal")
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == "testdata" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		f, err := parser.ParseFile(fset, p, nil, parser.ParseComments)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, filepath.Dir(p))
		if err != nil {
			return err
		}
		c.collect(f, path.Join(modulePath, filepath.ToSlash(rel)))
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("reading the Go doc comments: %w", err)
	}
	return c, nil
}

// FindModuleRoot returns the directory at or above dir whose go.mod
// declares modulePath.
func FindModuleRoot(dir, modulePath string) (string, error) {
	for d := dir; ; d = filepath.Dir(d) {
		data, err := os.ReadFile(filepath.Join(d, "go.mod"))
		if err == nil && strings.HasPrefix(string(data), "module "+modulePath+"\n") {
			return d, nil
		}
		if filepath.Dir(d) == d {
			return "", fmt.Errorf("no go.mod for %s at or above %s; run genschema from the repository", modulePath, dir)
		}
	}
}

// collect adds the type and field comments of file f, in package pkg.
func (c *Comments) collect(f *ast.File, pkg string) {
	for _, decl := range f.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.TYPE {
			continue
		}
		for _, spec := range gd.Specs {
			ts, ok := spec.(*ast.TypeSpec)
			if !ok {
				continue
			}
			doc := ts.Doc
			if doc == nil && len(gd.Specs) == 1 {
				doc = gd.Doc
			}
			key := pkg + "." + ts.Name.Name
			if text := doc.Text(); text != "" {
				c.byKey[key] = text
			}
			if st, ok := ts.Type.(*ast.StructType); ok {
				c.collectFields(st, key)
			}
		}
	}
}

// collectFields adds the field comments and JSON names of struct st, the
// type key.
func (c *Comments) collectFields(st *ast.StructType, key string) {
	for _, field := range st.Fields.List {
		text := field.Doc.Text()
		if text == "" {
			text = field.Comment.Text()
		}
		jsonName := ""
		if field.Tag != nil {
			tag := reflect.StructTag(strings.Trim(field.Tag.Value, "`"))
			if name, _, _ := strings.Cut(tag.Get("json"), ","); name != "-" {
				jsonName = name
			}
		}
		for _, n := range field.Names {
			fk := key + "." + n.Name
			if text != "" {
				c.byKey[fk] = text
			}
			if jsonName != "" {
				c.fields[fk] = jsonName
			}
		}
	}
}

// ticketRef matches a Linear ticket reference such as "(REF-130)", which
// means nothing to a reader of the schema.
var ticketRef = regexp.MustCompile(`\s*\(REF-\d+(, REF-\d+)*\)`)

// lookup returns the description of type t, or of its field.
func (c *Comments) lookup(t reflect.Type, field string) string {
	key := t.PkgPath() + "." + t.Name()
	if field != "" {
		key += "." + field
	}
	text, ok := c.byKey[key]
	if !ok {
		return ""
	}
	text = ticketRef.ReplaceAllString(text, "")
	if field != "" {
		// "AMIStatus is ..." reads better as "amiStatus is ...".
		if name, ok := c.fields[key]; ok && strings.HasPrefix(text, field) {
			rest := text[len(field):]
			if rest == "" || !isIdentRune([]rune(rest)[0]) {
				text = name + rest
			}
		}
	}
	return clean(text)
}

func isIdentRune(r rune) bool { return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r) }

// clean joins the lines of each paragraph of a doc comment with spaces and
// separates paragraphs with a blank line.
func clean(text string) string {
	var paras []string
	for _, p := range strings.Split(strings.TrimSpace(text), "\n\n") {
		paras = append(paras, strings.Join(strings.Fields(p), " "))
	}
	return strings.Join(paras, "\n\n")
}
