// Package cliconfig manages the persistent CLI context (active cluster,
// region, AWS profile) used to avoid passing --cluster on every invocation.
//
// Storage: $XDG_CONFIG_HOME/refresh/context.yaml (default
// ~/.config/refresh/context.yaml). The schema mirrors kubectx semantics:
// a named map of contexts, a current pointer, and a previous pointer for
// `refresh use -`.
package cliconfig

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"gopkg.in/yaml.v3"
)

var (
	userHomeDir = os.UserHomeDir
	readFile    = os.ReadFile
	mkdirAll    = os.MkdirAll
	removeFile  = os.Remove
	renameFile  = os.Rename
	marshalYAML = yaml.Marshal
)

type tempFile interface {
	Write([]byte) (int, error)
	Close() error
	Name() string
}

var createTemp = func(dir, pattern string) (tempFile, error) {
	return os.CreateTemp(dir, pattern)
}

// Context is a named pointer at an EKS cluster within a region/profile.
type Context struct {
	Cluster string `yaml:"cluster"`
	Region  string `yaml:"region,omitempty"`
	Profile string `yaml:"profile,omitempty"`
}

// File is the persisted YAML document.
type File struct {
	Current  string             `yaml:"current,omitempty"`
	Previous string             `yaml:"previous,omitempty"`
	Contexts map[string]Context `yaml:"contexts,omitempty"`
}

// Path returns the absolute path of the context file. The directory is
// not created here; Save creates it on demand.
func Path() (string, error) {
	if p := os.Getenv("REFRESH_CONFIG_HOME"); p != "" {
		return filepath.Join(p, "context.yaml"), nil
	}
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, "refresh", "context.yaml"), nil
	}
	home, err := userHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "refresh", "context.yaml"), nil
}

// Load reads the context file. A missing file returns an empty File and no error.
func Load() (*File, error) {
	p, err := Path()
	if err != nil {
		return nil, err
	}
	b, err := readFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return &File{Contexts: map[string]Context{}}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", p, err)
	}
	f := &File{}
	if err := yaml.Unmarshal(b, f); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", p, err)
	}
	if f.Contexts == nil {
		f.Contexts = map[string]Context{}
	}
	return f, nil
}

// Save writes the file atomically (write temp + rename) so a crash mid-write
// cannot corrupt an existing config.
func Save(f *File) error {
	p, err := Path()
	if err != nil {
		return err
	}
	if err := mkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	b, err := marshalYAML(f)
	if err != nil {
		return err
	}
	tmp, err := createTemp(filepath.Dir(p), ".context-*.yaml")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		_ = removeFile(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = removeFile(tmpName)
		return err
	}
	if err := renameFile(tmpName, p); err != nil {
		// Don't leave the orphaned temp file behind.
		_ = removeFile(tmpName)
		return err
	}
	return nil
}

// Names returns a sorted list of context names.
func (f *File) Names() []string {
	names := make([]string, 0, len(f.Contexts))
	for n := range f.Contexts {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// Active returns the currently selected context, if any.
//
// Resolution order: REFRESH_CONTEXT env var (per-shell override, kubectx-style)
// then File.Current. Returns ("", Context{}, false) when nothing is set.
func (f *File) Active() (name string, ctx Context, ok bool) {
	if env := os.Getenv("REFRESH_CONTEXT"); env != "" {
		if c, found := f.Contexts[env]; found {
			return env, c, true
		}
	}
	if f.Current != "" {
		if c, found := f.Contexts[f.Current]; found {
			return f.Current, c, true
		}
	}
	return "", Context{}, false
}

// Use sets `name` as current and rotates the previous pointer.
// "-" swaps current with previous (kubectx-style).
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

// Set inserts or replaces a context entry. It does not change the active context.
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

// Remove deletes a context entry. If it was current/previous, those pointers are cleared.
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
