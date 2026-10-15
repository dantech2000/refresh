package cmdutiltest

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"gopkg.in/yaml.v3"

	"github.com/dantech2000/refresh/internal/apidoc"
)

// Context returns a context that bounds every wait of a test, so that a
// missing handshake fails the test and does not hang it.
func Context(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// Golden compares got with the file testdata/name of the package under
// test. With update it writes the file in place of the comparison.
func Golden(t *testing.T, name, got string, update bool) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if update {
		if err := os.MkdirAll("testdata", 0o750); err != nil {
			t.Fatal(err)
		}
		// A golden file is source: readable like any other.
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil { //nolint:gosec // G306
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v; run the test with -update to write it", err)
	}
	if diff := cmp.Diff(string(want), got); diff != "" {
		t.Errorf("%s (-golden +got):\n%s", name, diff)
	}
}

// schemaBaseURL is the $id prefix of the committed schemas.
const schemaBaseURL = "https://drod.dev/refresh/schema/v2/"

// ValidateJSON checks a JSON document against the committed schema of its
// kind in api/schema/v2. The schema is closed first: a key that it does
// not list fails, so a field that the generator missed cannot hide.
func ValidateJSON(t *testing.T, kind apidoc.Kind, document []byte) {
	t.Helper()
	id := schemaBaseURL + string(kind) + ".json"
	f, err := os.Open(filepath.Join(moduleRoot(t), "api", "schema", "v2", string(kind)+".json"))
	if err != nil {
		t.Fatalf("%v; run: go run ./tools/genschema", err)
	}
	defer func() { _ = f.Close() }()
	schema, err := jsonschema.UnmarshalJSON(f)
	if err != nil {
		t.Fatal(err)
	}
	c := jsonschema.NewCompiler()
	c.DefaultDraft(jsonschema.Draft2020)
	c.AssertFormat()
	if err := c.AddResource(id, closeObjects(schema)); err != nil {
		t.Fatal(err)
	}
	compiled, err := c.Compile(id)
	if err != nil {
		t.Fatal(err)
	}
	instance, err := jsonschema.UnmarshalJSON(bytes.NewReader(document))
	if err != nil {
		t.Fatalf("the document is not JSON: %v\n%s", err, document)
	}
	if err := compiled.Validate(instance); err != nil {
		t.Errorf("the document does not match the schema of %s: %v", kind, err)
	}
}

// ValidateYAML checks a YAML document as ValidateJSON does.
func ValidateYAML(t *testing.T, kind apidoc.Kind, document []byte) {
	t.Helper()
	var v any
	if err := yaml.Unmarshal(document, &v); err != nil {
		t.Fatalf("the document is not YAML: %v", err)
	}
	asJSON, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	ValidateJSON(t, kind, asJSON)
}

// closeObjects sets additionalProperties to false on every object of a
// schema that lists properties.
func closeObjects(v any) any {
	switch x := v.(type) {
	case map[string]any:
		for k, val := range x {
			x[k] = closeObjects(val)
		}
		if _, ok := x["properties"]; ok {
			if _, set := x["additionalProperties"]; !set {
				x["additionalProperties"] = false
			}
		}
	case []any:
		for i, val := range x {
			x[i] = closeObjects(val)
		}
	}
	return v
}

// moduleRoot returns the directory of go.mod, at or above the directory of
// the package under test.
func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod above the package under test")
		}
		dir = parent
	}
}
