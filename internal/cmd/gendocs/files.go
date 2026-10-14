package gendocs

import (
	"fmt"
	"os"
	"path/filepath"
)

// generateFiles runs generate on a new, empty directory and then moves the
// files that it wrote there into dir, which must exist.
//
// Nothing in dir changes unless generate succeeds and no generated file
// would land on a directory, so a failed run leaves no partial output. Each
// file arrives by a rename. A rename replaces a symbolic link at the
// destination; it does not write through the link to its target.
// Entries in dir that generate did not write are not touched; the command
// removes its own stale pages afterwards.
func generateFiles(dir string, generate func(tmpDir string) error) error {
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
	// Check every file before the first rename, so a file that cannot be
	// installed stops the run while dir is still unchanged.
	for _, e := range entries {
		if !e.Type().IsRegular() {
			return fmt.Errorf("generated entry %s is not a regular file", e.Name())
		}
		dest := filepath.Join(dir, e.Name())
		if info, err := os.Lstat(dest); err == nil && info.IsDir() {
			return fmt.Errorf("%s is a directory; remove it and run the command again", dest)
		}
	}
	for _, e := range entries {
		if err := os.Rename(filepath.Join(tmp, e.Name()), filepath.Join(dir, e.Name())); err != nil {
			return err
		}
	}
	return nil
}
