package factory

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Commands get AWS clients from this package (NewEKSClient, NewSSMClient,
// the service constructors), never from an SDK NewFromConfig, so client
// setup has one home.
func TestCommandsBuildClientsThroughFactory(t *testing.T) {
	root := ".."
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == "factory" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for i, line := range strings.Split(string(src), "\n") {
			if strings.Contains(line, ".NewFromConfig(") {
				t.Errorf("%s:%d builds an SDK client directly; use a factory constructor: %s", path, i+1, strings.TrimSpace(line))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
