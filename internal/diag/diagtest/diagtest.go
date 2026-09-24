// Package diagtest checks that command output types report failures only as
// diag.Failure values. Tests call CheckFailureFields with the document types
// a package encodes.
package diagtest

import (
	"reflect"
	"slices"
	"strings"

	"github.com/dantech2000/refresh/internal/diag"
)

const modulePath = "github.com/dantech2000/refresh/"

// failureListKeys are the JSON keys that name a list of failures. A field
// with one of these keys must be a []diag.Failure (or a diag.List).
var failureListKeys = map[string]bool{
	"failures":        true,
	"errors":          true,
	"warnings":        true,
	"incomplete":      true,
	"discoveryErrors": true,
	"rollFailures":    true,
	"failed":          true,
}

// exempt are fields that use a failure key for something that is not a
// failure. They are permanent; the allow list of CheckFailureFields is for
// fields that are still to be migrated.
var exempt = map[string]string{
	// HealthSummary is a findings object: its warnings and errors are
	// health check results that drive exit 2 and 3, not failures to read.
	"health.HealthSummary.Warnings": "health findings",
	"health.HealthSummary.Errors":   "health findings",
}

var (
	failureType     = reflect.TypeFor[diag.Failure]()
	failurePtrType  = reflect.TypeFor[*diag.Failure]()
	jsonMarshaler   = reflect.TypeFor[interface{ MarshalJSON() ([]byte, error) }]()
	boolType        = reflect.TypeFor[bool]()
	failureListType = reflect.TypeFor[[]diag.Failure]()
)

// Offenders returns the fields reachable from roots that break the failure
// rule, as "pkg.Type.Field" strings in sorted order. A field breaks the rule
// when its JSON key is one of failures, errors, warnings, incomplete,
// discoveryErrors, rollFailures, or failed and its type is not a
// []diag.Failure, or when its key is failure and its type is not a
// diag.Failure. A bool "incomplete" flag on a row is allowed. Only struct
// types of this module are walked; the permanent exemptions (the
// HealthSummary findings) are left out.
func Offenders(roots ...reflect.Type) []string {
	seen := map[reflect.Type]bool{}
	var out []string
	var walk func(reflect.Type)
	walk = func(typ reflect.Type) {
		for typ.Kind() == reflect.Pointer || typ.Kind() == reflect.Slice || typ.Kind() == reflect.Array || typ.Kind() == reflect.Map {
			typ = typ.Elem()
		}
		if typ.Kind() != reflect.Struct || seen[typ] || !strings.HasPrefix(typ.PkgPath(), modulePath) {
			return
		}
		seen[typ] = true
		if typ == failureType || typ.Implements(jsonMarshaler) || reflect.PointerTo(typ).Implements(jsonMarshaler) {
			return
		}
		for i := range typ.NumField() {
			f := typ.Field(i)
			if !f.IsExported() && !f.Anonymous {
				continue // encoding/json skips it (it does promote an unexported embedded struct's fields)
			}
			key, _, _ := strings.Cut(f.Tag.Get("json"), ",")
			if key == "-" {
				continue
			}
			if key == "" && !f.Anonymous {
				key = f.Name
			}
			name := typ.String() + "." + f.Name
			if _, ok := exempt[name]; !ok && breaksRule(key, f.Type) {
				out = append(out, name)
			}
			walk(f.Type)
		}
	}
	for _, root := range roots {
		walk(root)
	}
	slices.Sort(out)
	return out
}

// breaksRule reports whether a field with JSON key key and type typ breaks
// the failure rule.
func breaksRule(key string, typ reflect.Type) bool {
	switch {
	case key == "failure":
		return typ != failureType && typ != failurePtrType
	case key == "incomplete" && typ == boolType:
		return false
	case failureListKeys[key]:
		return !typ.ConvertibleTo(failureListType)
	}
	return false
}

// Reporter is the part of testing.TB that CheckFailureFields uses.
type Reporter interface {
	Helper()
	Errorf(format string, args ...any)
}

// CheckFailureFields fails t for each offender reachable from roots (see
// Offenders) that allow does not list, and for each allow entry that is no
// longer an offender, so a migrated field must leave the list. allow maps
// "pkg.Type.Field" to the issue that migrates it.
func CheckFailureFields(t Reporter, allow map[string]string, roots ...reflect.Type) {
	t.Helper()
	found := map[string]bool{}
	for _, name := range Offenders(roots...) {
		found[name] = true
		if _, ok := allow[name]; !ok {
			t.Errorf("%s reports failures without diag.Failure: use []diag.Failure (diag.List) under \"failures\", or diag.Failure under \"failure\"", name)
		}
	}
	for name, issue := range allow {
		if !found[name] {
			t.Errorf("allow list entry %s (%s) is not an offender any more; delete it", name, issue)
		}
	}
}
