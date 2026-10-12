// Package contexts stores the named refresh contexts: which cluster, region,
// and AWS profile a name stands for, and which context is active.
//
// The store is one YAML file.
package contexts

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// EnvContext names the environment variable that selects the active context
// for one shell. It beats the current context in the file.
const EnvContext = "REFRESH_CONTEXT"

// Context points a name at an EKS cluster in a region and an AWS profile.
type Context struct {
	Cluster string `yaml:"cluster"`
	Region  string `yaml:"region,omitempty"`
	Profile string `yaml:"profile,omitempty"`
}

// File is the content of the store.
type File struct {
	Current  string             `yaml:"current,omitempty"`
	Previous string             `yaml:"previous,omitempty"` // for "refresh context use -"
	Contexts map[string]Context `yaml:"contexts,omitempty"`
}

// Store reads and writes the context file at Path.
//
// Save replaces the file in one step, so a reader sees the old file or the
// new one. Nothing locks the file: when two commands change it at the same
// time, the one that saves last wins and the change of the other is lost.
type Store struct {
	Path string
}

// DefaultStore returns the store of the user. Its file is context.yaml in
// $REFRESH_CONFIG_HOME when that is set. Otherwise the file is
// refresh/context.yaml in $XDG_CONFIG_HOME, which defaults to ~/.config.
func DefaultStore() (*Store, error) {
	if dir := os.Getenv("REFRESH_CONFIG_HOME"); dir != "" {
		return &Store{Path: filepath.Join(dir, "context.yaml")}, nil
	}
	if dir := os.Getenv("XDG_CONFIG_HOME"); dir != "" {
		return &Store{Path: filepath.Join(dir, "refresh", "context.yaml")}, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("finding the context file: %w", err)
	}
	return &Store{Path: filepath.Join(home, ".config", "refresh", "context.yaml")}, nil
}

// Load reads the file. A missing file is an empty File and no error.
func (s *Store) Load() (*File, error) {
	b, err := os.ReadFile(s.Path)
	if errors.Is(err, os.ErrNotExist) {
		return &File{Contexts: map[string]Context{}}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", s.Path, err)
	}
	f := &File{}
	// A key that this version does not know is an error. Dropping it here
	// would delete it from the file at the next Save.
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	if err := dec.Decode(f); err != nil && !errors.Is(err, io.EOF) { // EOF: an empty file
		var typeErr *yaml.TypeError
		if errors.As(err, &typeErr) {
			return nil, fmt.Errorf("parsing %s: %s", s.Path, strings.Join(typeErr.Errors, "; "))
		}
		return nil, fmt.Errorf("parsing %s: %w", s.Path, err)
	}
	if f.Contexts == nil {
		f.Contexts = map[string]Context{}
	}
	return f, nil
}

// Save writes the file. It writes a temporary file and renames it, so a
// crash in the middle cannot damage the file that is there.
func (s *Store) Save(f *File) error {
	dir := filepath.Dir(s.Path)
	if err := os.MkdirAll(dir, 0o755); err != nil { //nolint:gosec // G301: the usual mode of a config directory; the file holds no secret
		return err
	}
	b, err := yaml.Marshal(f)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".context-*.yaml")
	if err != nil {
		return err
	}
	_, err = tmp.Write(b)
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(tmp.Name(), s.Path)
	}
	if err != nil {
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("writing %s: %w", s.Path, err)
	}
	return nil
}

// Names returns the context names in alphabetical order.
func (f *File) Names() []string {
	names := make([]string, 0, len(f.Contexts))
	for n := range f.Contexts {
		names = append(names, n)
	}
	slices.Sort(names)
	return names
}

// Active returns the context that commands use. override is the value of
// REFRESH_CONTEXT; when it is empty, the current context of the file is
// active. ok is false when no context is active.
//
// An override that names no saved context is an error. A fallback to the
// current context would point every command at the wrong cluster after a
// typing error. A current context that is not saved is an error too: the
// file was edited by hand, and the user must choose a context again.
func (f *File) Active(override string) (name string, c Context, ok bool, err error) {
	if override != "" {
		if c, found := f.Contexts[override]; found {
			return override, c, true, nil
		}
		known := "none saved"
		if names := f.Names(); len(names) > 0 {
			known = strings.Join(names, ", ")
		}
		return "", Context{}, false, fmt.Errorf("unknown context %q from %s; known: %s", override, EnvContext, known)
	}
	if f.Current == "" {
		return "", Context{}, false, nil
	}
	c, found := f.Contexts[f.Current]
	if !found {
		return "", Context{}, false, fmt.Errorf("the current context %q is not a saved context; choose one with: refresh context use <name>", f.Current)
	}
	return f.Current, c, true, nil
}

// has reports whether name is a saved context.
func (f *File) has(name string) bool {
	_, ok := f.Contexts[name]
	return ok
}

// Use makes name the current context and remembers the one before it. The
// name "-" swaps the current context with the previous one.
//
// A current or previous pointer that names no saved context counts as not
// set, so Use never keeps or switches to a context that does not exist.
func (f *File) Use(name string) error {
	old := f.Current
	if !f.has(old) {
		old = ""
	}
	if name == "-" {
		if !f.has(f.Previous) {
			return errors.New("no previous context to switch to")
		}
		f.Current, f.Previous = f.Previous, old
		return nil
	}
	if !f.has(name) {
		return fmt.Errorf("unknown context %q", name)
	}
	if old != name {
		f.Previous = old
	}
	f.Current = name
	return nil
}

// Set adds the context, or replaces the one with the same name. It does not
// change the current context.
func (f *File) Set(name string, c Context) error {
	if name == "" {
		return errors.New("context name is required")
	}
	if c.Cluster == "" {
		return errors.New("cluster is required")
	}
	if f.Contexts == nil {
		f.Contexts = map[string]Context{}
	}
	f.Contexts[name] = c
	return nil
}

// Remove deletes the context. When it was the current or the previous
// context, that pointer is cleared.
func (f *File) Remove(name string) error {
	if _, ok := f.Contexts[name]; !ok {
		return fmt.Errorf("unknown context %q", name)
	}
	delete(f.Contexts, name)
	if f.Current == name {
		f.Current = ""
	}
	if f.Previous == name {
		f.Previous = ""
	}
	return nil
}
