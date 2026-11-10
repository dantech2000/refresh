package cmdutil

import (
	"fmt"
	"slices"
	"strings"

	"github.com/spf13/cobra"
)

// Filter is one key=pattern of the -f/--filter flag.
type Filter struct {
	// Key is one of the filter keys of the command, as the command spells
	// it.
	Key string
	// Pattern is what the value must match. "*" stands for any run of
	// characters, and nothing else is special.
	Pattern string
}

// Filters are the filters of one run. An item is selected when every one
// of them matches.
type Filters []Filter

// AddFilterFlag adds -f/--filter to cmd. The flag is repeatable. Each value
// is key=pattern, and is parsed into p as cobra reads it: the key must be
// one of keys, in any case, and the pattern must not be empty. Spaces
// around the key and around the pattern do not count. A value that does
// not parse is an error and never a filter that matches nothing.
func AddFilterFlag(cmd *cobra.Command, p *Filters, keys ...string) {
	cmd.Flags().VarP(&filterValue{filters: p, keys: keys}, "filter", "f",
		"Select by key=value, where * matches any text; repeatable, and all must match. Keys: "+strings.Join(keys, ", "))
}

// filterValue is the pflag.Value of the filter flag.
type filterValue struct {
	filters *Filters
	keys    []string
}

// String returns the filters as pflag shows a string array, and "" for
// none, so that the help shows no default.
func (v *filterValue) String() string {
	if len(*v.filters) == 0 {
		return ""
	}
	parts := make([]string, len(*v.filters))
	for i, f := range *v.filters {
		parts[i] = f.String()
	}
	return "[" + strings.Join(parts, ",") + "]"
}

// Type is the type that the help shows. A filter is a string that repeats.
func (v *filterValue) Type() string { return "stringArray" }

func (v *filterValue) Set(raw string) error {
	key, pattern, ok := strings.Cut(raw, "=")
	key, pattern = strings.TrimSpace(key), strings.TrimSpace(pattern)
	if !ok || key == "" || pattern == "" {
		return fmt.Errorf("invalid filter %q: want key=value, with one of the keys %s", raw, strings.Join(v.keys, ", "))
	}
	i := slices.IndexFunc(v.keys, func(k string) bool { return strings.EqualFold(k, key) })
	if i < 0 {
		return fmt.Errorf("invalid filter %q: unknown key %q; the keys are %s", raw, key, strings.Join(v.keys, ", "))
	}
	*v.filters = append(*v.filters, Filter{Key: v.keys[i], Pattern: pattern})
	return nil
}

// String returns the filter as the flag takes it: key=pattern.
func (f Filter) String() string { return f.Key + "=" + f.Pattern }

// String returns the filters as flags, such as "-f name=web-* -f
// status=ACTIVE", for a message that names the selection.
func (fs Filters) String() string {
	parts := make([]string, len(fs))
	for i, f := range fs {
		parts[i] = "-f " + f.String()
	}
	return strings.Join(parts, " ")
}

// Match reports whether every filter matches. get returns the values of
// the item for a key. A key with several values matches when one of them
// does.
func (fs Filters) Match(get func(key string) []string) bool {
	for _, f := range fs {
		if !slices.ContainsFunc(get(f.Key), f.matches) {
			return false
		}
	}
	return true
}

// Only returns the filters with one of keys.
func (fs Filters) Only(keys ...string) Filters {
	var out Filters
	for _, f := range fs {
		if slices.Contains(keys, f.Key) {
			out = append(out, f)
		}
	}
	return out
}

// matches reports whether value matches the pattern, in any case. Without
// a "*" the pattern must equal the whole value.
func (f Filter) matches(value string) bool {
	value = strings.ToLower(value)
	parts := strings.Split(strings.ToLower(f.Pattern), "*")
	if len(parts) == 1 {
		return value == parts[0]
	}
	// The first part is a prefix and the last a suffix. The parts between
	// them must follow one another in what is left.
	first, last := parts[0], parts[len(parts)-1]
	if len(value) < len(first)+len(last) || !strings.HasPrefix(value, first) || !strings.HasSuffix(value, last) {
		return false
	}
	rest := value[len(first) : len(value)-len(last)]
	for _, part := range parts[1 : len(parts)-1] {
		i := strings.Index(rest, part)
		if i < 0 {
			return false
		}
		rest = rest[i+len(part):]
	}
	return true
}
