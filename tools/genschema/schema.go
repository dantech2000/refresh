package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"

	"github.com/invopop/jsonschema"

	"github.com/dantech2000/refresh/internal/apidoc"
)

// schemaBaseURL is where the schemas are published. The $id of a schema is
// this URL and the file name.
const schemaBaseURL = "https://drod.dev/refresh/schema/v2/"

// failuresDescription describes the failures list at the top of a document.
const failuresDescription = "Every failure of the run: the items that refresh could not read and the actions that did not complete. " +
	"The list is sorted by kind, region, cluster, name, and operation. It is [] when there are no failures."

var enumType = reflect.TypeFor[apidoc.Enum]()

// generate returns the JSON Schema (draft 2020-12) of doc, indented, with a
// line break at the end. The rules of the schema are the rules of the
// documents:
//
//   - apiVersion and kind are required constants and come first.
//   - A key without omitzero or omitempty is required.
//   - No key allows null.
//   - A type that implements apidoc.Enum is a string with its values.
//   - An object allows keys that the schema does not list, because a later
//     release may add one.
//   - The descriptions are the Go doc comments.
func generate(doc apidoc.Document, docs *comments) ([]byte, error) {
	g := &generator{docs: docs, names: map[string]reflect.Type{}, enums: map[string]reflect.Type{}}
	r := &jsonschema.Reflector{
		ExpandedStruct:            true,
		AllowAdditionalProperties: true,
		Anonymous:                 true,
		Namer:                     g.name,
		Mapper:                    g.mapEnum,
		LookupComment:             g.docs.lookup,
	}
	s := r.ReflectFromType(reflect.TypeOf(doc))
	if g.err != nil {
		return nil, g.err
	}
	for name, t := range g.enums {
		enum, ok := reflect.Zero(t).Interface().(apidoc.Enum)
		if !ok {
			return nil, fmt.Errorf("%s is not an enum", t)
		}
		var values []any
		for _, v := range enum.Values() {
			values = append(values, v)
		}
		s.Definitions[name] = &jsonschema.Schema{Type: "string", Enum: values, Description: g.docs.lookup(t, "")}
	}
	if len(s.Definitions) == 0 {
		s.Definitions = nil
	}

	kind := doc.Kind()
	s.ID = jsonschema.ID(schemaBaseURL + string(kind) + ".json")
	s.Title = string(kind)
	s.Description = fmt.Sprintf("The document that `refresh %s -o json` prints. -o yaml prints the same structure.", kind.Command())
	props := jsonschema.NewProperties()
	props.Set("apiVersion", &jsonschema.Schema{Type: "string", Const: apidoc.APIVersion, Description: "The version of the document contract."})
	props.Set("kind", &jsonschema.Schema{Type: "string", Const: string(kind), Description: "The type of the document."})
	for p := s.Properties.Oldest(); p != nil; p = p.Next() {
		if p.Key == "failures" {
			p.Value.Description = failuresDescription
		}
		props.Set(p.Key, p.Value)
	}
	s.Properties = props
	s.Required = append([]string{"apiVersion", "kind"}, s.Required...)

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(s); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// generator holds what the schema of one document collects while the
// reflector walks its types.
type generator struct {
	docs *comments
	// names maps each $defs name to its Go type, to catch two types with
	// one name.
	names map[string]reflect.Type
	// enums are the enum types that the schema refers to, by $defs name.
	enums map[string]reflect.Type
	err   error
}

// name returns the $defs name of t: its Go name. Two types with one name
// are an error.
func (g *generator) name(t reflect.Type) string {
	name := t.Name()
	if name == "" {
		return ""
	}
	if prev, ok := g.names[name]; ok && prev != t && g.err == nil {
		g.err = fmt.Errorf("two types have the schema name %s: %s and %s", name, prev, t)
	}
	g.names[name] = t
	return name
}

// mapEnum returns a reference to the definition of t when t is an enum.
// The definitions are added after the walk.
func (g *generator) mapEnum(t reflect.Type) *jsonschema.Schema {
	if t.Kind() == reflect.Pointer || !t.Implements(enumType) {
		return nil
	}
	name := g.name(t)
	g.enums[name] = t
	return &jsonschema.Schema{Ref: "#/$defs/" + name}
}
