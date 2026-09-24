package commands

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/dantech2000/refresh/internal/apidoc"
	"github.com/dantech2000/refresh/internal/commands/documents"
	"github.com/dantech2000/refresh/internal/schemagen"
)

// modulePath is refresh's Go module path; gen-docs reads the doc comments
// of its source for the schema descriptions.
const modulePath = "github.com/dantech2000/refresh"

// writeSchemas writes the JSON Schema of every document kind to schemaDir
// (<Kind>.json) and the page that lists them to refDir/schemas.md. It
// removes a stale <Kind>.json of a kind that no longer exists.
func writeSchemas(refDir, schemaDir string) error {
	wd, err := os.Getwd()
	if err != nil {
		return err
	}
	root, err := schemagen.FindModuleRoot(wd, modulePath)
	if err != nil {
		return err
	}
	comments, err := schemagen.LoadComments(root, modulePath)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(schemaDir, genDocDirMode); err != nil {
		return fmt.Errorf("creating %s: %w", schemaDir, err)
	}
	want := map[string]bool{}
	for _, doc := range documents.All() {
		data, err := schemagen.Generate(doc, comments)
		if err != nil {
			return err
		}
		name := string(doc.DocumentKind()) + ".json"
		want[name] = true
		if err := writeFile(filepath.Join(schemaDir, name), string(data)); err != nil {
			return err
		}
	}
	stale, err := filepath.Glob(filepath.Join(schemaDir, "*.json"))
	if err != nil {
		return err
	}
	for _, p := range stale {
		if !want[filepath.Base(p)] {
			if err := os.Remove(p); err != nil {
				return err
			}
		}
	}
	rel, err := filepath.Rel(refDir, schemaDir)
	if err != nil {
		return err
	}
	return writeFile(filepath.Join(refDir, "schemas.md"), renderSchemaIndex(filepath.ToSlash(rel)))
}

// renderSchemaIndex is the reference page that lists each document kind
// with its command and schema. schemaPath is the schema directory relative
// to the page.
func renderSchemaIndex(schemaPath string) string {
	var b strings.Builder
	b.WriteString(generatedNote)
	b.WriteString("# JSON schemas\n\n")
	fmt.Fprintf(&b, "Every `-o json` and `-o yaml` document starts with `apiVersion: %s` and a `kind`. ", apidoc.APIVersion)
	b.WriteString("Each kind has a JSON Schema (draft 2020-12), generated from the Go types, ")
	fmt.Fprintf(&b, "at `%s<kind>.json`. ", apidoc.SchemaBaseURL)
	b.WriteString("The [output contract](../concepts/output.md#compatibility) says what can change within `v1`.\n\n")
	b.WriteString("| Kind | Printed by | Schema |\n|---|---|---|\n")
	for _, k := range apidoc.Kinds() {
		fmt.Fprintf(&b, "| `%s` | `refresh %s` | [%s.json](%s/%s.json) |\n", k, k.Command(), k, schemaPath, k)
	}
	b.WriteString("\nValidate a document with any draft 2020-12 validator, for example:\n\n")
	b.WriteString("```bash\nrefresh cluster list -o json > clusters.json\n")
	fmt.Fprintf(&b, "check-jsonschema --schemafile %sClusterList.json clusters.json\n```\n", apidoc.SchemaBaseURL)
	return b.String()
}
