package contexts

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestDefaultStore(t *testing.T) {
	tests := []struct {
		name       string
		configHome string
		xdg        string
		home       string
		want       string
	}{
		{
			name:       "REFRESH_CONFIG_HOME",
			configHome: "/cfg", xdg: "/xdg", home: "/home/u",
			want: "/cfg/context.yaml",
		},
		{name: "XDG_CONFIG_HOME", xdg: "/xdg", home: "/home/u", want: "/xdg/refresh/context.yaml"},
		{name: "home", home: "/home/u", want: "/home/u/.config/refresh/context.yaml"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("REFRESH_CONFIG_HOME", tt.configHome)
			t.Setenv("XDG_CONFIG_HOME", tt.xdg)
			t.Setenv("HOME", tt.home)
			t.Setenv("USERPROFILE", tt.home) // the home directory on Windows

			s, err := DefaultStore()
			if err != nil {
				t.Fatalf("DefaultStore() error = %v", err)
			}
			if want := filepath.FromSlash(tt.want); s.Path != want {
				t.Errorf("Path = %q, want %q", s.Path, want)
			}
		})
	}
}

func TestLoad(t *testing.T) {
	tests := []struct {
		name    string
		content *string // nil means no file
		want    *File
		wantErr string
	}{
		{name: "missing file", want: &File{Contexts: map[string]Context{}}},
		{name: "empty file", content: ptr(""), want: &File{Contexts: map[string]Context{}}},
		{
			name:    "no contexts key",
			content: ptr("current: prod\n"),
			want:    &File{Current: "prod", Contexts: map[string]Context{}},
		},
		{
			// A file that refresh 0.x wrote. The keys must not change.
			name: "file of an earlier release",
			content: ptr(`current: prod
previous: staging
contexts:
    prod:
        cluster: prod-cluster
        region: us-east-1
        profile: prod-admin
    staging:
        cluster: staging-cluster
`),
			want: &File{
				Current:  "prod",
				Previous: "staging",
				Contexts: map[string]Context{
					"prod":    {Cluster: "prod-cluster", Region: "us-east-1", Profile: "prod-admin"},
					"staging": {Cluster: "staging-cluster"},
				},
			},
		},
		{name: "invalid YAML", content: ptr("contexts: [not, a, map"), wantErr: "parsing "},
		{
			name:    "unknown key",
			content: ptr("current: prod\ndefault-region: us-east-1\ncontexts:\n    prod:\n        cluster: p\n"),
			wantErr: "line 2: field default-region not found",
		},
		{
			name:    "unknown key in a context",
			content: ptr("contexts:\n    prod:\n        cluster: p\n        role: admin\n"),
			wantErr: "line 4: field role not found",
		},
		{name: "comments only", content: ptr("# no contexts yet\n"), want: &File{Contexts: map[string]Context{}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &Store{Path: filepath.Join(t.TempDir(), "context.yaml")}
			if tt.content != nil {
				if err := os.WriteFile(s.Path, []byte(*tt.content), 0o600); err != nil {
					t.Fatal(err)
				}
			}

			got, err := s.Load()

			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) || !strings.Contains(err.Error(), s.Path) {
					t.Fatalf("Load() error = %v, want one that contains %q and the path", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Load() error = %v", err)
			}
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("Load() (-want +got):\n%s", diff)
			}
		})
	}
}

func TestLoadUnreadableFile(t *testing.T) {
	// A directory at the path of the file cannot be read as a file.
	s := &Store{Path: t.TempDir()}
	if _, err := s.Load(); err == nil || !strings.Contains(err.Error(), "reading "+s.Path) {
		t.Errorf("Load() error = %v, want a read error that names the path", err)
	}
}

func TestSave(t *testing.T) {
	dir := t.TempDir()
	// The directory does not exist yet: Save makes it.
	s := &Store{Path: filepath.Join(dir, "refresh", "context.yaml")}
	f := &File{
		Current:  "prod",
		Previous: "staging",
		Contexts: map[string]Context{
			"prod":    {Cluster: "prod-cluster", Region: "us-east-1", Profile: "prod-admin"},
			"staging": {Cluster: "staging-cluster", Region: "us-west-2"},
		},
	}

	if err := s.Save(f); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	// The exact text is the format that every release reads.
	want := `current: prod
previous: staging
contexts:
    prod:
        cluster: prod-cluster
        region: us-east-1
        profile: prod-admin
    staging:
        cluster: staging-cluster
        region: us-west-2
`
	b, err := os.ReadFile(s.Path)
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(want, string(b)); diff != "" {
		t.Errorf("file content (-want +got):\n%s", diff)
	}
	got, err := s.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if diff := cmp.Diff(f, got); diff != "" {
		t.Errorf("Load() after Save() (-want +got):\n%s", diff)
	}
	assertOnlyFile(t, filepath.Dir(s.Path), "context.yaml")
}

func TestSaveReplaces(t *testing.T) {
	s := &Store{Path: filepath.Join(t.TempDir(), "context.yaml")}
	for _, cluster := range []string{"first", "second"} {
		f := &File{Contexts: map[string]Context{"prod": {Cluster: cluster}}}
		if err := s.Save(f); err != nil {
			t.Fatalf("Save(%s) error = %v", cluster, err)
		}
	}
	got, err := s.Load()
	if err != nil {
		t.Fatal(err)
	}
	if c := got.Contexts["prod"].Cluster; c != "second" {
		t.Errorf("cluster = %q, want second", c)
	}
	assertOnlyFile(t, filepath.Dir(s.Path), "context.yaml")
}

func TestSaveErrors(t *testing.T) {
	t.Run("directory cannot be made", func(t *testing.T) {
		blocker := filepath.Join(t.TempDir(), "blocker")
		if err := os.WriteFile(blocker, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		s := &Store{Path: filepath.Join(blocker, "refresh", "context.yaml")}
		if err := s.Save(&File{}); err == nil {
			t.Error("Save() error = nil, want an error: a file is in the way of the directory")
		}
	})
	t.Run("file cannot be replaced", func(t *testing.T) {
		dir := t.TempDir()
		s := &Store{Path: filepath.Join(dir, "context.yaml")}
		// A directory that is not empty cannot be renamed over.
		if err := os.MkdirAll(filepath.Join(s.Path, "keep"), 0o700); err != nil {
			t.Fatal(err)
		}
		err := s.Save(&File{})
		if err == nil || !strings.Contains(err.Error(), "writing "+s.Path) {
			t.Errorf("Save() error = %v, want a write error that names the path", err)
		}
		// The temporary file is gone.
		assertOnlyFile(t, dir, "context.yaml")
	})
}

func assertOnlyFile(t *testing.T, dir, want string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if diff := cmp.Diff([]string{want}, names); diff != "" {
		t.Errorf("files in %s (-want +got):\n%s", dir, diff)
	}
}

func TestUse(t *testing.T) {
	abc := map[string]Context{"a": {Cluster: "ca"}, "b": {Cluster: "cb"}, "c": {Cluster: "cc"}}
	tests := []struct {
		name         string
		uses         []string
		wantCurrent  string
		wantPrevious string
		wantErr      string
	}{
		{name: "first use", uses: []string{"a"}, wantCurrent: "a"},
		{name: "second use remembers the first", uses: []string{"a", "b"}, wantCurrent: "b", wantPrevious: "a"},
		{name: "same name twice keeps the previous", uses: []string{"a", "b", "b"}, wantCurrent: "b", wantPrevious: "a"},
		{name: "dash swaps", uses: []string{"a", "b", "-"}, wantCurrent: "a", wantPrevious: "b"},
		{name: "dash twice returns", uses: []string{"a", "b", "-", "-"}, wantCurrent: "b", wantPrevious: "a"},
		{
			name: "dash without a previous", uses: []string{"a", "-"},
			wantCurrent: "a", wantErr: "no previous context to switch to",
		},
		{
			name: "unknown name", uses: []string{"a", "nope"},
			wantCurrent: "a", wantErr: `unknown context "nope"`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &File{Contexts: abc}
			var err error
			for _, name := range tt.uses {
				err = f.Use(name)
			}
			if tt.wantErr == "" && err != nil {
				t.Fatalf("Use() error = %v", err)
			}
			if tt.wantErr != "" && (err == nil || err.Error() != tt.wantErr) {
				t.Fatalf("Use() error = %v, want %q", err, tt.wantErr)
			}
			if f.Current != tt.wantCurrent || f.Previous != tt.wantPrevious {
				t.Errorf("current = %q, previous = %q; want %q, %q", f.Current, f.Previous, tt.wantCurrent, tt.wantPrevious)
			}
		})
	}
}

// TestUseWithDanglingPointers: a pointer that names no saved context, from
// a file edited by hand, is never switched to and never kept.
func TestUseWithDanglingPointers(t *testing.T) {
	saved := map[string]Context{"a": {Cluster: "ca"}, "b": {Cluster: "cb"}}
	tests := []struct {
		name         string
		file         File
		use          string
		wantCurrent  string
		wantPrevious string
		wantErr      string
	}{
		{
			name: "dash with a dangling previous",
			file: File{Current: "a", Previous: "gone", Contexts: saved}, use: "-",
			wantCurrent: "a", wantPrevious: "gone", wantErr: "no previous context to switch to",
		},
		{
			name: "dash with a dangling current",
			file: File{Current: "gone", Previous: "b", Contexts: saved}, use: "-",
			wantCurrent: "b",
		},
		{
			name: "name with a dangling current",
			file: File{Current: "gone", Contexts: saved}, use: "a",
			wantCurrent: "a",
		},
		{
			name: "name with a dangling current replaces an older previous",
			file: File{Current: "gone", Previous: "b", Contexts: saved}, use: "a",
			wantCurrent: "a",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := tt.file
			err := f.Use(tt.use)
			if tt.wantErr == "" && err != nil {
				t.Fatalf("Use() error = %v", err)
			}
			if tt.wantErr != "" && (err == nil || err.Error() != tt.wantErr) {
				t.Fatalf("Use() error = %v, want %q", err, tt.wantErr)
			}
			if f.Current != tt.wantCurrent || f.Previous != tt.wantPrevious {
				t.Errorf("current = %q, previous = %q; want %q, %q", f.Current, f.Previous, tt.wantCurrent, tt.wantPrevious)
			}
		})
	}
}

func TestActive(t *testing.T) {
	saved := map[string]Context{"prod": {Cluster: "p"}, "staging": {Cluster: "s"}}
	tests := []struct {
		name     string
		file     File
		override string
		wantName string
		wantErr  string
	}{
		{name: "current of the file", file: File{Current: "prod", Contexts: saved}, wantName: "prod"},
		{name: "override beats current", file: File{Current: "prod", Contexts: saved}, override: "staging", wantName: "staging"},
		{name: "override without a current", file: File{Contexts: saved}, override: "staging", wantName: "staging"},
		{name: "nothing set", file: File{Contexts: saved}},
		{
			// A file edited by hand. The old pointer is reported, not used
			// and not read as "no context".
			name: "current names no context", file: File{Current: "missing", Contexts: saved},
			wantErr: `the current context "missing" is not a saved context; choose one with: refresh use <name>`,
		},
		{
			name: "override beats a current that names no context",
			file: File{Current: "missing", Contexts: saved}, override: "prod", wantName: "prod",
		},
		{
			// A typing error must not fall back to the current context:
			// that would point "stagee" at prod.
			name: "unknown override", file: File{Current: "prod", Contexts: saved}, override: "stagee",
			wantErr: `unknown context "stagee" from REFRESH_CONTEXT; known: prod, staging`,
		},
		{
			name: "unknown override, empty store", file: File{}, override: "prod",
			wantErr: `unknown context "prod" from REFRESH_CONTEXT; known: none saved`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			name, c, ok, err := tt.file.Active(tt.override)
			if tt.wantErr != "" {
				if err == nil || err.Error() != tt.wantErr {
					t.Errorf("Active() error = %v, want %q", err, tt.wantErr)
				}
				if ok || name != "" || c != (Context{}) {
					t.Errorf("Active() = %q, %+v, %t with an error; want no context", name, c, ok)
				}
				return
			}
			if err != nil {
				t.Fatalf("Active() error = %v", err)
			}
			if name != tt.wantName || ok != (tt.wantName != "") {
				t.Errorf("Active() = %q, %t; want %q", name, ok, tt.wantName)
			}
			if c != saved[tt.wantName] {
				t.Errorf("Active() context = %+v, want %+v", c, saved[tt.wantName])
			}
		})
	}
}

func TestNames(t *testing.T) {
	f := &File{Contexts: map[string]Context{"z": {Cluster: "z"}, "a": {Cluster: "a"}, "m": {Cluster: "m"}}}
	if diff := cmp.Diff([]string{"a", "m", "z"}, f.Names()); diff != "" {
		t.Errorf("Names() (-want +got):\n%s", diff)
	}
	if got := (&File{}).Names(); len(got) != 0 {
		t.Errorf("Names() of an empty file = %v, want none", got)
	}
}

func TestSet(t *testing.T) {
	f := &File{} // a nil map: Set makes it
	if err := f.Set("", Context{Cluster: "c"}); err == nil || err.Error() != "context name is required" {
		t.Errorf("Set without a name: error = %v", err)
	}
	if err := f.Set("prod", Context{}); err == nil || err.Error() != "cluster is required" {
		t.Errorf("Set without a cluster: error = %v", err)
	}
	if err := f.Set("prod", Context{Cluster: "one"}); err != nil {
		t.Fatalf("Set() error = %v", err)
	}
	if err := f.Set("prod", Context{Cluster: "two", Region: "eu-west-1"}); err != nil {
		t.Fatalf("Set() of the same name: error = %v", err)
	}
	want := &File{Contexts: map[string]Context{"prod": {Cluster: "two", Region: "eu-west-1"}}}
	if diff := cmp.Diff(want, f); diff != "" {
		t.Errorf("file after Set() (-want +got):\n%s", diff)
	}
}

func TestRemove(t *testing.T) {
	newFile := func() *File {
		return &File{
			Current:  "a",
			Previous: "b",
			Contexts: map[string]Context{"a": {Cluster: "ca"}, "b": {Cluster: "cb"}, "c": {Cluster: "cc"}},
		}
	}
	tests := []struct {
		name         string
		remove       string
		wantCurrent  string
		wantPrevious string
		wantErr      string
	}{
		{name: "current", remove: "a", wantPrevious: "b"},
		{name: "previous", remove: "b", wantCurrent: "a"},
		{name: "other", remove: "c", wantCurrent: "a", wantPrevious: "b"},
		{name: "unknown", remove: "nope", wantCurrent: "a", wantPrevious: "b", wantErr: `unknown context "nope"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFile()
			err := f.Remove(tt.remove)
			if tt.wantErr != "" {
				if err == nil || err.Error() != tt.wantErr {
					t.Fatalf("Remove() error = %v, want %q", err, tt.wantErr)
				}
			} else if err != nil {
				t.Fatalf("Remove() error = %v", err)
			}
			if _, found := f.Contexts[tt.remove]; found {
				t.Errorf("context %q is still in the file", tt.remove)
			}
			if f.Current != tt.wantCurrent || f.Previous != tt.wantPrevious {
				t.Errorf("current = %q, previous = %q; want %q, %q", f.Current, f.Previous, tt.wantCurrent, tt.wantPrevious)
			}
		})
	}
}

func ptr(s string) *string { return &s }
