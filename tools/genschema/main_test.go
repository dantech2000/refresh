package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/dantech2000/refresh/internal/apidoc"
)

// schemaDir is the directory of the committed schemas, from this package.
const schemaDir = "../../api/schema/v2"

// The committed schemas are what the generator writes now. After a change
// to a document type, run: go run ./tools/genschema
func TestCommittedSchemasAreFresh(t *testing.T) {
	schemas, err := generateAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(schemas) != len(apidoc.Kinds()) {
		t.Errorf("generated %d schemas for %d kinds", len(schemas), len(apidoc.Kinds()))
	}
	for name, want := range schemas {
		got, err := os.ReadFile(filepath.Join(schemaDir, name))
		if err != nil {
			t.Errorf("%s: %v; run: go run ./tools/genschema", name, err)
			continue
		}
		if diff := cmp.Diff(string(want), string(got)); diff != "" {
			t.Errorf("%s is stale; run: go run ./tools/genschema\n(-generated +committed):\n%s", name, diff)
		}
	}
	committed, err := filepath.Glob(filepath.Join(schemaDir, "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range committed {
		if _, ok := schemas[filepath.Base(p)]; !ok {
			t.Errorf("%s is the schema of a kind that no longer exists; run: go run ./tools/genschema", p)
		}
	}
}

// Kinds, Documents, and Command list the same kinds in the same order.
func TestEveryKindHasADocumentAndACommand(t *testing.T) {
	docs := apidoc.Documents()
	kinds := apidoc.Kinds()
	if len(docs) != len(kinds) {
		t.Fatalf("%d documents for %d kinds", len(docs), len(kinds))
	}
	for i, doc := range docs {
		if doc.Kind() != kinds[i] {
			t.Errorf("document %d is %s, want %s", i, doc.Kind(), kinds[i])
		}
		if kinds[i].Command() == "" {
			t.Errorf("%s has no command", kinds[i])
		}
	}
}
