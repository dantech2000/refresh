// Package schemagen generates the JSON Schema (draft 2020-12) of each
// document refresh prints with -o json|yaml, from the Go types.
//
// The schemas describe the v1 contract (see package apidoc):
//
//   - apiVersion and kind are required constants and come first.
//   - A field without omitempty is required; a field with omitempty is
//     optional.
//   - No field allows null. A document prints [] for a list it read and
//     found empty, and leaves out (omitempty) what it did not collect.
//   - A type that implements apidoc.Enum is a string with an enum list.
//   - Objects allow unknown properties, because a later v1 release may add
//     fields.
//   - Descriptions come from the Go doc comments of the types and fields.
//
// The genschema tool (internal/tools/genschema, run by `task docs:gen`)
// writes the schemas to docs/schema/v1, and a CI step fails when the
// committed files are stale. The refresh binary does not import this
// package.
package schemagen

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"unicode"

	"github.com/invopop/jsonschema"

	"github.com/dantech2000/refresh/internal/apidoc"
	"github.com/dantech2000/refresh/internal/diag"
)

// failuresDescription is the description of a document's top-level
// failures list.
const failuresDescription = "Every failure of the run: the items refresh could not read and the actions that did not complete. " +
	"This top-level list is complete; a nested failure or failures field repeats a subset of it. " +
	"The list is sorted by kind, region, cluster, name, and operation. [] when there are no failures."

// typeNames overrides the $defs name of a type whose Go name is too
// generic for a schema.
var typeNames = map[reflect.Type]string{
	reflect.TypeFor[diag.List]():   "FailureList",
	reflect.TypeFor[diag.Kind]():   "FailureKind",
	reflect.TypeFor[diag.Reason](): "FailureReason",
}

var (
	enumType = reflect.TypeFor[apidoc.Enum]()
)

// Generate returns the schema of doc, indented, with a trailing newline.
// comments are the Go doc comments (see Comments); nil leaves the
// descriptions out.
func Generate(doc apidoc.Document, comments *Comments) ([]byte, error) {
	g := &generator{comments: comments, names: map[string]reflect.Type{}, types: map[reflect.Type]string{}, enums: map[string]reflect.Type{}}
	s, err := g.schema(doc)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(s); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// generator builds one document's schema.
type generator struct {
	comments *Comments
	// names maps each $defs name to its Go type, to catch two types with the
	// same name; types is the reverse.
	names map[string]reflect.Type
	types map[reflect.Type]string
	// enums are the enum types the schema refers to, by $defs name.
	enums map[string]reflect.Type
	err   error
}

func (g *generator) schema(doc apidoc.Document) (*jsonschema.Schema, error) {
	root := reflect.TypeOf(doc)
	if root.Kind() == reflect.Pointer {
		root = root.Elem()
	}
	r := &jsonschema.Reflector{
		ExpandedStruct:            true,
		AllowAdditionalProperties: true,
		Anonymous:                 true,
		Namer:                     g.name,
		Mapper:                    g.mapEnum,
		LookupComment:             g.comment,
	}
	s := r.ReflectFromType(root)
	if g.err != nil {
		return nil, g.err
	}
	for name, t := range g.enums {
		e, ok := reflect.Zero(t).Interface().(apidoc.Enum)
		if !ok {
			return nil, fmt.Errorf("%s is not an enum", t)
		}
		values := e.EnumValues()
		enum := make([]any, len(values))
		for i, v := range values {
			enum[i] = v
		}
		s.Definitions[name] = &jsonschema.Schema{Type: "string", Enum: enum, Description: g.comment(t, "")}
	}
	if len(s.Definitions) == 0 {
		s.Definitions = nil
	}

	kind := doc.DocumentKind()
	s.ID = jsonschema.ID(apidoc.SchemaBaseURL + string(kind) + ".json")
	s.Title = string(kind)
	s.Description = fmt.Sprintf("The document that `refresh %s -o json` prints. -o yaml prints the same structure.", kind.Command())

	props := jsonschema.NewProperties()
	props.Set("apiVersion", &jsonschema.Schema{Type: "string", Const: apidoc.APIVersion, Description: "The version of the document contract."})
	props.Set("kind", &jsonschema.Schema{Type: "string", Const: string(kind), Description: "The type of the document."})
	for p := s.Properties.Oldest(); p != nil; p = p.Next() {
		if p.Key == "apiVersion" || p.Key == "kind" {
			return nil, fmt.Errorf("%s: the document has its own %q field", kind, p.Key)
		}
		if p.Key == "failures" {
			p.Value.Description = failuresDescription
		}
		props.Set(p.Key, p.Value)
	}
	s.Properties = props
	s.Required = append([]string{"apiVersion", "kind"}, s.Required...)
	return s, nil
}

// name is the $defs name of t: its Go name with a capital first letter,
// unless typeNames overrides it. Two types with the same name are an error.
func (g *generator) name(t reflect.Type) string {
	name, ok := typeNames[t]
	if !ok {
		name = t.Name()
		if name == "" {
			return ""
		}
		r := []rune(name)
		r[0] = unicode.ToUpper(r[0])
		name = string(r)
	}
	if prev, ok := g.names[name]; ok && prev != t && g.err == nil {
		g.err = fmt.Errorf("two types have the schema name %s: %s and %s", name, prev, t)
	}
	g.names[name] = t
	g.types[t] = name
	return name
}

// mapEnum returns a reference to the enum definition of t, when t is an
// enum; the definitions are added after reflection.
func (g *generator) mapEnum(t reflect.Type) *jsonschema.Schema {
	if !t.Implements(enumType) || t.Kind() == reflect.Pointer {
		return nil
	}
	name := g.name(t)
	g.enums[name] = t
	return &jsonschema.Schema{Ref: "#/$defs/" + name}
}

// typeDescriptions overrides the description of a type whose doc comment
// is about Go, not about the document.
var typeDescriptions = map[reflect.Type]string{
	reflect.TypeFor[diag.List](): "A list of failures. It is [] when there are none, never null.",
}

// comment is the description of type t, or of its field when field is set.
// A type description that starts with the Go type name starts with the
// schema name instead.
func (g *generator) comment(t reflect.Type, field string) string {
	if field == "" {
		if d, ok := typeDescriptions[t]; ok {
			return d
		}
	}
	if g.comments == nil {
		return ""
	}
	text := g.comments.lookup(t, field)
	if field == "" && t.Name() != "" && strings.HasPrefix(text, t.Name()+" ") {
		text = g.name(t) + strings.TrimPrefix(text, t.Name())
	}
	return text
}
