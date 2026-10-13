// Command genschema writes the JSON Schema of every document that refresh
// prints with -o json and -o yaml to api/schema/v2/<Kind>.json. Run it from
// the repository after a change to a document type; a test fails when a
// committed schema differs from its output.
//
// It is a program of its own, not a refresh subcommand, so the schema
// generator and its dependencies stay out of the refresh binary.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/dantech2000/refresh/internal/apidoc"
)

// modulePath is the Go module of refresh. The descriptions in the schemas
// are the doc comments of its document types.
const modulePath = "github.com/dantech2000/refresh"

const (
	fileMode = 0o644
	dirMode  = 0o755
)

func main() {
	out := flag.String("out", "api/schema/v2", "the directory to write the schemas to")
	flag.Parse()
	if err := run(*out); err != nil {
		fmt.Fprintln(os.Stderr, "genschema:", err)
		os.Exit(1)
	}
}

// run writes the schema of every document kind to dir, and removes the
// schema of a kind that no longer exists.
func run(dir string) error {
	schemas, err := generateAll()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, dirMode); err != nil {
		return fmt.Errorf("creating %s: %w", dir, err)
	}
	for name, data := range schemas {
		if err := os.WriteFile(filepath.Join(dir, name), data, fileMode); err != nil {
			return err
		}
	}
	old, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		return err
	}
	for _, p := range old {
		if _, ok := schemas[filepath.Base(p)]; !ok {
			if err := os.Remove(p); err != nil {
				return err
			}
		}
	}
	fmt.Printf("Wrote %d JSON schemas to %s\n", len(schemas), dir)
	return nil
}

// generateAll returns the schema of every document kind by its file name.
func generateAll() (map[string][]byte, error) {
	wd, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	root, err := findModuleRoot(wd)
	if err != nil {
		return nil, err
	}
	docs, err := loadComments(root, "internal/apidoc")
	if err != nil {
		return nil, err
	}
	schemas := map[string][]byte{}
	for _, doc := range apidoc.Documents() {
		data, err := generate(doc, docs)
		if err != nil {
			return nil, err
		}
		schemas[string(doc.Kind())+".json"] = data
	}
	return schemas, nil
}

// findModuleRoot returns the directory at or above dir whose go.mod
// declares the module of refresh.
func findModuleRoot(dir string) (string, error) {
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
