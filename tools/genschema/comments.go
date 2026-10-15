package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path"
	"path/filepath"
	"reflect"
	"strings"
	"unicode"
)

// comments holds the Go doc comments of the document types and their
// fields, for the descriptions in the schemas.
type comments struct {
	// text maps "importpath.Type" and "importpath.Type.Field" to the
	// comment.
	text map[string]string
	// jsonNames maps "importpath.Type.Field" to the JSON name of the
	// field, so that a comment that starts with the Go name of the field
	// can start with its JSON name.
	jsonNames map[string]string
}

// loadComments parses the Go files of the package in dir, a path below the
// module root. Test files are left out.
func loadComments(root, dir string) (*comments, error) {
	c := &comments{text: map[string]string{}, jsonNames: map[string]string{}}
	files, err := filepath.Glob(filepath.Join(root, dir, "*.go"))
	if err != nil {
		return nil, err
	}
	fset := token.NewFileSet()
	for _, p := range files {
		if strings.HasSuffix(p, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, p, nil, parser.ParseComments)
		if err != nil {
			return nil, fmt.Errorf("reading the Go doc comments: %w", err)
		}
		c.collect(f, path.Join(modulePath, filepath.ToSlash(dir)))
	}
	return c, nil
}

// collect adds the comments of the types and fields of file f, in package
// pkg.
func (c *comments) collect(f *ast.File, pkg string) {
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
				c.text[key] = text
			}
			if st, ok := ts.Type.(*ast.StructType); ok {
				c.collectFields(st, key)
			}
		}
	}
}

// collectFields adds the comments and the JSON names of the fields of st,
// the type with the key.
func (c *comments) collectFields(st *ast.StructType, key string) {
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
				c.text[fk] = text
			}
			if jsonName != "" {
				c.jsonNames[fk] = jsonName
			}
		}
	}
}

// lookup returns the description of type t, or of its field when field is
// set. A field comment that starts with the Go name of the field starts
// with its JSON name in the result: "amiStatus is ...".
func (c *comments) lookup(t reflect.Type, field string) string {
	key := t.PkgPath() + "." + t.Name()
	if field != "" {
		key += "." + field
	}
	text, ok := c.text[key]
	if !ok {
		return ""
	}
	if name, ok := c.jsonNames[key]; ok && strings.HasPrefix(text, field) {
		rest := text[len(field):]
		if r := []rune(rest); len(r) == 0 || (r[0] != '_' && !unicode.IsLetter(r[0]) && !unicode.IsDigit(r[0])) {
			text = name + rest
		}
	}
	// One line for each paragraph, and a blank line between paragraphs.
	var paras []string
	for _, p := range strings.Split(strings.TrimSpace(text), "\n\n") {
		paras = append(paras, strings.Join(strings.Fields(p), " "))
	}
	return strings.Join(paras, "\n\n")
}
