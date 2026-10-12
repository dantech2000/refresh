package cmdutil

import (
	"fmt"
	"os"
	"path/filepath"
)

// GenerateFiles runs generate on a new, empty directory and then moves the
// files that it wrote there into dir, which must exist.
//
// Nothing in dir changes unless generate succeeds, so a failed run leaves no
// partial output. Each file arrives by a rename. A rename replaces a symbolic
// link at the destination; it does not write through the link to its target.
// Entries in dir that generate did not write are not touched.
func GenerateFiles(dir string, generate func(tmpDir string) error) error {
	// The temporary directory is inside dir: a rename needs both ends on
	// one file system.
	tmp, err := os.MkdirTemp(dir, ".refresh-tmp-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(tmp) }()

	if err := generate(tmp); err != nil {
		return err
	}
	entries, err := os.ReadDir(tmp)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if !e.Type().IsRegular() {
			return fmt.Errorf("generated entry %s is not a regular file", e.Name())
		}
		if err := os.Rename(filepath.Join(tmp, e.Name()), filepath.Join(dir, e.Name())); err != nil {
			return err
		}
	}
	return nil
}
