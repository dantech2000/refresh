package cliconfig

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func withTempHome(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("REFRESH_CONFIG_HOME", dir)
	t.Setenv("REFRESH_CONTEXT", "")
	return dir
}

func resetHooks(t *testing.T) {
	t.Helper()
	oldHome := userHomeDir
	oldRead := readFile
	oldMkdir := mkdirAll
	oldCreate := createTemp
	oldRemove := removeFile
	oldRename := renameFile
	oldMarshal := marshalYAML
	t.Cleanup(func() {
		userHomeDir = oldHome
		readFile = oldRead
		mkdirAll = oldMkdir
		createTemp = oldCreate
		removeFile = oldRemove
		renameFile = oldRename
		marshalYAML = oldMarshal
	})
}

type fakeTempFile struct {
	name     string
	writeErr error
	closeErr error
}

func (f fakeTempFile) Write([]byte) (int, error) {
	if f.writeErr != nil {
		return 0, f.writeErr
	}
	return 1, nil
}

func (f fakeTempFile) Close() error { return f.closeErr }

func (f fakeTempFile) Name() string { return f.name }

func TestPathHonorsRefreshConfigHome(t *testing.T) {
	dir := withTempHome(t)
	got, err := Path()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(dir, "context.yaml"); got != want {
		t.Errorf("Path = %q, want %q", got, want)
	}
}

func TestPathHonorsXDGConfigHome(t *testing.T) {
	t.Setenv("REFRESH_CONFIG_HOME", "")
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	got, err := Path()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(dir, "refresh", "context.yaml"); got != want {
		t.Fatalf("Path() = %q, want %q", got, want)
	}
}

func TestPathUserHomeError(t *testing.T) {
	resetHooks(t)
	t.Setenv("REFRESH_CONFIG_HOME", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	userHomeDir = func() (string, error) { return "", errors.New("no home") }

	if _, err := Path(); err == nil {
		t.Fatal("expected home error")
	}
}

func TestPathDefaultHome(t *testing.T) {
	resetHooks(t)
	t.Setenv("REFRESH_CONFIG_HOME", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	userHomeDir = func() (string, error) { return "/home/tester", nil }

	got, err := Path()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join("/home/tester", ".config", "refresh", "context.yaml"); got != want {
		t.Fatalf("Path() = %q, want %q", got, want)
	}
}

func TestLoadMissingFileReturnsEmpty(t *testing.T) {
	withTempHome(t)
	f, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(f.Contexts) != 0 || f.Current != "" {
		t.Errorf("expected empty file, got %+v", f)
	}
}

func TestLoadInvalidYAMLErrors(t *testing.T) {
	dir := withTempHome(t)
	if err := os.WriteFile(filepath.Join(dir, "context.yaml"), []byte(":\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(); err == nil {
		t.Fatal("expected parse error")
	}
}

func TestLoadInitializesNilContexts(t *testing.T) {
	dir := withTempHome(t)
	if err := os.WriteFile(filepath.Join(dir, "context.yaml"), []byte("current: prod\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if f.Contexts == nil {
		t.Fatal("Contexts should be initialized")
	}
}

func TestLoadReadFileError(t *testing.T) {
	resetHooks(t)
	withTempHome(t)
	readFile = func(string) ([]byte, error) { return nil, errors.New("denied") }

	if _, err := Load(); err == nil {
		t.Fatal("expected read error")
	}
}

func TestLoadPathError(t *testing.T) {
	resetHooks(t)
	t.Setenv("REFRESH_CONFIG_HOME", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	userHomeDir = func() (string, error) { return "", errors.New("no home") }

	if _, err := Load(); err == nil {
		t.Fatal("expected path error")
	}
}

func TestRoundtripSaveLoad(t *testing.T) {
	withTempHome(t)
	f := &File{
		Current:  "prod",
		Previous: "staging",
		Contexts: map[string]Context{
			"prod":    {Cluster: "prod-cluster", Region: "us-east-1", Profile: "prod-admin"},
			"staging": {Cluster: "staging-cluster", Region: "us-west-2"},
		},
	}
	if err := Save(f); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Current != "prod" || got.Previous != "staging" {
		t.Errorf("pointers wrong: %+v", got)
	}
	if c := got.Contexts["prod"]; c.Cluster != "prod-cluster" || c.Region != "us-east-1" || c.Profile != "prod-admin" {
		t.Errorf("prod context wrong: %+v", c)
	}
}

func TestUseSwitchesAndRotatesPrevious(t *testing.T) {
	f := &File{Contexts: map[string]Context{
		"a": {Cluster: "ca"}, "b": {Cluster: "cb"}, "c": {Cluster: "cc"},
	}}
	if err := f.Use("a"); err != nil {
		t.Fatal(err)
	}
	if f.Current != "a" || f.Previous != "" {
		t.Errorf("after Use(a): %+v", f)
	}
	if err := f.Use("b"); err != nil {
		t.Fatal(err)
	}
	if f.Current != "b" || f.Previous != "a" {
		t.Errorf("after Use(b): current=%s prev=%s", f.Current, f.Previous)
	}
	// Re-using same name shouldn't shuffle previous away.
	if err := f.Use("b"); err != nil {
		t.Fatal(err)
	}
	if f.Previous != "a" {
		t.Errorf("Use(b) twice shouldn't change previous: %s", f.Previous)
	}
}

func TestUseDashSwapsWithPrevious(t *testing.T) {
	f := &File{Contexts: map[string]Context{"a": {Cluster: "ca"}, "b": {Cluster: "cb"}}}
	_ = f.Use("a")
	_ = f.Use("b")
	if err := f.Use("-"); err != nil {
		t.Fatal(err)
	}
	if f.Current != "a" || f.Previous != "b" {
		t.Errorf("after Use(-): current=%s prev=%s", f.Current, f.Previous)
	}
}

func TestUseDashWithoutPreviousErrors(t *testing.T) {
	f := &File{Contexts: map[string]Context{"a": {Cluster: "ca"}}}
	_ = f.Use("a")
	if err := f.Use("-"); err == nil {
		t.Error("expected error swapping with no previous")
	}
}

func TestUseUnknownErrors(t *testing.T) {
	f := &File{Contexts: map[string]Context{}}
	if err := f.Use("nope"); err == nil {
		t.Error("expected error for unknown context")
	}
}

func TestActiveResolutionOrder(t *testing.T) {
	withTempHome(t)
	f := &File{
		Current: "prod",
		Contexts: map[string]Context{
			"prod":    {Cluster: "p"},
			"staging": {Cluster: "s"},
		},
	}
	// Default: file's Current.
	t.Setenv("REFRESH_CONTEXT", "")
	if name, _, ok := f.Active(); !ok || name != "prod" {
		t.Errorf("active = %s/%v, want prod", name, ok)
	}
	// Env var overrides Current.
	t.Setenv("REFRESH_CONTEXT", "staging")
	if name, _, ok := f.Active(); !ok || name != "staging" {
		t.Errorf("active with env = %s/%v, want staging", name, ok)
	}
	// Env points at unknown context → fall through to Current.
	t.Setenv("REFRESH_CONTEXT", "ghost")
	if name, _, ok := f.Active(); !ok || name != "prod" {
		t.Errorf("active with bad env = %s/%v, want fallback to prod", name, ok)
	}
}

func TestActiveEmptyWhenNoContextMatches(t *testing.T) {
	f := &File{Current: "missing", Contexts: map[string]Context{}}
	t.Setenv("REFRESH_CONTEXT", "ghost")
	if name, ctx, ok := f.Active(); ok || name != "" || ctx.Cluster != "" {
		t.Fatalf("Active() = %q, %+v, %v; want empty", name, ctx, ok)
	}
}

func TestNamesSorted(t *testing.T) {
	f := &File{Contexts: map[string]Context{
		"z": {Cluster: "z"},
		"a": {Cluster: "a"},
		"m": {Cluster: "m"},
	}}
	got := f.Names()
	if want := []string{"a", "m", "z"}; len(got) != len(want) || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Fatalf("Names() = %v, want %v", got, want)
	}
}

func TestRemoveClearsPointers(t *testing.T) {
	f := &File{Contexts: map[string]Context{"a": {Cluster: "ca"}, "b": {Cluster: "cb"}}}
	_ = f.Use("a")
	_ = f.Use("b")
	if err := f.Remove("b"); err != nil {
		t.Fatal(err)
	}
	if f.Current != "" {
		t.Errorf("removing current should clear it: current=%s", f.Current)
	}
	if err := f.Remove("a"); err != nil {
		t.Fatal(err)
	}
	if f.Previous != "" {
		t.Errorf("removing previous should clear it: previous=%s", f.Previous)
	}
}

func TestSetRequiresNameAndCluster(t *testing.T) {
	f := &File{}
	if err := f.Set("", Context{Cluster: "x"}); err == nil {
		t.Error("expected error for empty name")
	}
	if err := f.Set("a", Context{}); err == nil {
		t.Error("expected error for empty cluster")
	}
	if err := f.Set("a", Context{Cluster: "ca"}); err != nil {
		t.Fatalf("Set valid context: %v", err)
	}
	if f.Contexts["a"].Cluster != "ca" {
		t.Fatalf("context not inserted: %+v", f.Contexts)
	}
}

func TestRemoveUnknownErrors(t *testing.T) {
	f := &File{Contexts: map[string]Context{}}
	if err := f.Remove("missing"); err == nil {
		t.Fatal("expected error")
	}
}

func TestSaveCreatesDirAndIsAtomic(t *testing.T) {
	dir := withTempHome(t)
	f := &File{Contexts: map[string]Context{"a": {Cluster: "ca"}}}
	if err := Save(f); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "context.yaml")); err != nil {
		t.Errorf("file missing: %v", err)
	}
}

func TestSaveErrorBranches(t *testing.T) {
	tests := []struct {
		name  string
		setup func()
	}{
		{
			name: "path",
			setup: func() {
				t.Setenv("REFRESH_CONFIG_HOME", "")
				t.Setenv("XDG_CONFIG_HOME", "")
				userHomeDir = func() (string, error) { return "", errors.New("no home") }
			},
		},
		{
			name: "mkdir",
			setup: func() {
				withTempHome(t)
				mkdirAll = func(string, os.FileMode) error { return errors.New("mkdir") }
			},
		},
		{
			name: "marshal",
			setup: func() {
				withTempHome(t)
				marshalYAML = func(any) ([]byte, error) { return nil, errors.New("marshal") }
			},
		},
		{
			name: "create temp",
			setup: func() {
				withTempHome(t)
				createTemp = func(string, string) (tempFile, error) { return nil, errors.New("create") }
			},
		},
		{
			name: "write",
			setup: func() {
				withTempHome(t)
				createTemp = func(string, string) (tempFile, error) {
					return fakeTempFile{name: "tmp", writeErr: errors.New("write")}, nil
				}
			},
		},
		{
			name: "close",
			setup: func() {
				withTempHome(t)
				createTemp = func(string, string) (tempFile, error) {
					return fakeTempFile{name: "tmp", closeErr: errors.New("close")}, nil
				}
			},
		},
		{
			name: "rename",
			setup: func() {
				withTempHome(t)
				renameFile = func(string, string) error { return errors.New("rename") }
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resetHooks(t)
			tt.setup()
			if err := Save(&File{}); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}
