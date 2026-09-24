package fakeaws

import (
	"fmt"
	"reflect"
	"testing"
)

// RequireFailures fails tb unless the decoded document doc (from
// RequireOneDocument) has a top-level "failures" list with exactly
// len(want) entries, and entry i has every key of want[i] with the same
// value. Other keys of an entry are not checked. It returns the entries.
// With no want, it checks that "failures" is present and empty ([]).
func RequireFailures(tb testing.TB, doc any, want ...map[string]any) []map[string]any {
	tb.Helper()
	obj, ok := doc.(map[string]any)
	if !ok {
		tb.Fatalf("document is %T, want an object with a failures key", doc)
	}
	raw, ok := obj["failures"]
	if !ok {
		tb.Fatalf("document has no failures key: %v", obj)
	}
	list, ok := raw.([]any)
	if !ok {
		tb.Fatalf("failures is %T (%v), want a list", raw, raw)
	}
	got := make([]map[string]any, 0, len(list))
	for i, e := range list {
		m, ok := e.(map[string]any)
		if !ok {
			tb.Fatalf("failures[%d] is %T, want an object", i, e)
		}
		got = append(got, m)
	}
	if len(got) != len(want) {
		tb.Fatalf("failures has %d entries, want %d:\n%s", len(got), len(want), describe(got))
	}
	for i, w := range want {
		for k, v := range w {
			if !reflect.DeepEqual(got[i][k], v) {
				tb.Errorf("failures[%d].%s = %#v, want %#v\n%s", i, k, got[i][k], v, describe(got))
			}
		}
	}
	return got
}

func describe(fs []map[string]any) string {
	out := ""
	for i, f := range fs {
		out += fmt.Sprintf("  [%d] %v\n", i, f)
	}
	return out
}
