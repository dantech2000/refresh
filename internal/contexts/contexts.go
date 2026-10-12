// Package contexts stores the named refresh contexts: which cluster, region,
// and AWS profile a name stands for, and which context is active.
//
// The store is one YAML file. A user's file from an earlier release of
// refresh must keep working, so the keys and the location do not change.
package contexts

import (
	"errors"
	"fmt"
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
	Previous string             `yaml:"previous,omitempty"` // for "refresh use -"
	Contexts map[string]Context `yaml:"contexts,omitempty"`
}

// Store reads and writes the context file at Path.
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
	if err := yaml.Unmarshal(b, f); err != nil {
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
// typing error.
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
	if c, found := f.Contexts[f.Current]; found && f.Current != "" {
		return f.Current, c, true, nil
	}
	return "", Context{}, false, nil
}

// Use makes name the current context and remembers the one before it. The
// name "-" swaps the current context with the previous one.
func (f *File) Use(name string) error {
	if name == "-" {
		if f.Previous == "" {
			return errors.New("no previous context to switch to")
		}
		f.Current, f.Previous = f.Previous, f.Current
		return nil
	}
	if _, ok := f.Contexts[name]; !ok {
		return fmt.Errorf("unknown context %q", name)
	}
	if f.Current != name {
		f.Previous = f.Current
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
