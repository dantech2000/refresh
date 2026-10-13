package cmdutil_test

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/spf13/cobra"

	"github.com/dantech2000/refresh/internal/apidoc"
	"github.com/dantech2000/refresh/internal/cmdutil"
)

func TestParseFilters(t *testing.T) {
	keys := []string{"name", "instanceType"}
	tests := []struct {
		name    string
		raw     []string
		want    cmdutil.Filters
		wantErr string
	}{
		{name: "none", want: cmdutil.Filters{}},
		{name: "one", raw: []string{"name=prod-*"}, want: cmdutil.Filters{{Key: "name", Pattern: "prod-*"}}},
		{
			name: "the key in any case, spelled as the command spells it",
			raw:  []string{"INSTANCETYPE=m5.large", " Name =a"},
			want: cmdutil.Filters{{Key: "instanceType", Pattern: "m5.large"}, {Key: "name", Pattern: "a"}},
		},
		{name: "a value with an equals sign", raw: []string{"name=a=b"}, want: cmdutil.Filters{{Key: "name", Pattern: "a=b"}}},
		{name: "a key twice", raw: []string{"name=a*", "name=*b"}, want: cmdutil.Filters{{Key: "name", Pattern: "a*"}, {Key: "name", Pattern: "*b"}}},
		{name: "no equals sign", raw: []string{"prod"}, wantErr: `invalid filter "prod": want key=value, with one of the keys name, instanceType`},
		{name: "no key", raw: []string{"=prod"}, wantErr: `invalid filter "=prod": want key=value, with one of the keys name, instanceType`},
		{name: "spaces around the pattern", raw: []string{"name= prod* "}, want: cmdutil.Filters{{Key: "name", Pattern: "prod*"}}},
		{name: "a value of spaces", raw: []string{"name=  "}, wantErr: `invalid filter "name=  ": want key=value, with one of the keys name, instanceType`},
		{name: "no value", raw: []string{"name="}, wantErr: `invalid filter "name=": want key=value, with one of the keys name, instanceType`},
		{name: "unknown key", raw: []string{"nam=prod"}, wantErr: `invalid filter "nam=prod": unknown key "nam"; the keys are name, instanceType`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := cmdutil.ParseFilters(tt.raw, keys...)
			if tt.wantErr != "" {
				if err == nil || err.Error() != tt.wantErr {
					t.Fatalf("error = %v, want %s", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if diff := cmp.Diff(tt.want, got); diff != "" {
				t.Errorf("filters (-want +got):\n%s", diff)
			}
		})
	}
}

func TestFilters_Match(t *testing.T) {
	tests := []struct {
		pattern string
		value   string
		want    bool
	}{
		{"prod", "prod", true},
		{"prod", "PROD", true},
		// Without a star the whole value must match: no substring match.
		{"prod", "prod-eu", false},
		{"prod", "my-prod", false},
		{"prod-*", "prod-eu", true},
		{"prod-*", "prod-", true},
		{"prod-*", "prod", false},
		{"*-eu", "prod-eu", true},
		{"*prod*", "my-prod-eu", true},
		{"*prod*", "prod", true},
		{"a*b*c", "a-b-c", true},
		{"a*b*c", "abc", true},
		{"a*b*c", "acb", false},
		// The prefix and the suffix must not share characters.
		{"ab*ba", "aba", false},
		{"ab*ba", "abba", true},
		{"*", "anything", true},
		{"*", "", true},
		{"**", "x", true},
		// Nothing but the star is special.
		{"m5.?", "m5.x", false},
		{"m5.?", "m5.?", true},
		{"[ab]", "a", false},
		{"1.3*", "1.33", true},
	}
	for _, tt := range tests {
		fs := cmdutil.Filters{{Key: "k", Pattern: tt.pattern}}
		got := fs.Match(func(string) []string { return []string{tt.value} })
		if got != tt.want {
			t.Errorf("pattern %q, value %q: match = %v, want %v", tt.pattern, tt.value, got, tt.want)
		}
	}
}

func TestFilters_EveryFilterMustMatchAndOneValueIsEnough(t *testing.T) {
	item := map[string][]string{"name": {"web-spot"}, "instanceType": {"m5.large", "c5.large"}}
	get := func(key string) []string { return item[key] }
	tests := []struct {
		name string
		fs   cmdutil.Filters
		want bool
	}{
		{"no filter", nil, true},
		{"both match", cmdutil.Filters{{Key: "name", Pattern: "web-*"}, {Key: "name", Pattern: "*-spot"}}, true},
		{"one does not", cmdutil.Filters{{Key: "name", Pattern: "web-*"}, {Key: "name", Pattern: "*-od"}}, false},
		{"one of several values", cmdutil.Filters{{Key: "instanceType", Pattern: "c5.*"}}, true},
		{"none of several values", cmdutil.Filters{{Key: "instanceType", Pattern: "r5.*"}}, false},
	}
	for _, tt := range tests {
		if got := tt.fs.Match(get); got != tt.want {
			t.Errorf("%s: match = %v, want %v", tt.name, got, tt.want)
		}
	}

	fs := cmdutil.Filters{{Key: "name", Pattern: "a"}, {Key: "instanceType", Pattern: "b"}, {Key: "name", Pattern: "c"}}
	want := cmdutil.Filters{{Key: "name", Pattern: "a"}, {Key: "name", Pattern: "c"}}
	if diff := cmp.Diff(want, fs.Only("name")); diff != "" {
		t.Errorf("Only (-want +got):\n%s", diff)
	}
}

func TestSortFlags(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		wantKey  string
		wantDesc bool
		wantErr  string
	}{
		{name: "default", wantKey: "name"},
		{name: "a key", args: []string{"--sort", "version", "--desc"}, wantKey: "version", wantDesc: true},
		{name: "a key in another case", args: []string{"--sort", "VERSION"}, wantKey: "version"},
		{name: "unknown key", args: []string{"--sort", "age"}, wantErr: `invalid argument "age" for "--sort" flag: must be one of: name, version`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var key string
			var desc bool
			cmd := &cobra.Command{Use: "x", RunE: func(*cobra.Command, []string) error { return nil }, SilenceErrors: true, SilenceUsage: true}
			cmdutil.AddSortFlags(cmd, &key, &desc, "name", "version")
			cmd.SetArgs(tt.args)
			err := cmd.Execute()
			if tt.wantErr != "" {
				if err == nil || err.Error() != tt.wantErr {
					t.Fatalf("error = %v, want %s", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if key != tt.wantKey || desc != tt.wantDesc {
				t.Errorf("key = %q, desc = %v, want %q, %v", key, desc, tt.wantKey, tt.wantDesc)
			}
		})
	}
}

func TestIncompleteError(t *testing.T) {
	if err := cmdutil.IncompleteError(nil); err != nil {
		t.Errorf("no failures: error = %v, want nil", err)
	}
	err := cmdutil.IncompleteError([]apidoc.Failure{
		{Kind: apidoc.FailureRegion}, {Kind: apidoc.FailureCluster}, {Kind: apidoc.FailureRegion},
	})
	if got := cmdutil.ExitCode(err); got != cmdutil.ExitIncomplete {
		t.Errorf("exit code = %d, want %d", got, cmdutil.ExitIncomplete)
	}
	if want := "incomplete data: 3 failure(s) (1 cluster, 2 regions)"; err.Error() != want {
		t.Errorf("error = %q, want %q", err.Error(), want)
	}
}
