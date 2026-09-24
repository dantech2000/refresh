package diagtest

import (
	"reflect"
	"slices"
	"testing"

	"github.com/dantech2000/refresh/internal/diag"
)

type row struct {
	Name       string        `json:"name"`
	Incomplete bool          `json:"incomplete,omitempty"` // a bool flag is allowed
	Failure    *diag.Failure `json:"failure,omitempty"`
	Errors     []string      `json:"errors,omitempty"` // offender
}

type badFailure struct {
	Failure string `json:"failure"` // offender
}

type embedded struct {
	Failed []string `json:"failed"` // offender, reached through embedding
}

type doc struct {
	embedded
	Rows     []row          `json:"rows"`
	Bad      *badFailure    `json:"bad"`
	Failures diag.List      `json:"failures"`
	Warnings []diag.Failure `json:"warnings"`
	Hidden   []string       `json:"-"`
	Notices  []string       `json:"notices"`
}

func TestOffenders(t *testing.T) {
	got := Offenders(reflect.TypeFor[doc]())
	want := []string{
		"diagtest.badFailure.Failure",
		"diagtest.embedded.Failed",
		"diagtest.row.Errors",
	}
	if !slices.Equal(got, want) {
		t.Errorf("Offenders = %v, want %v", got, want)
	}
}

// recorder is a Reporter that counts errors.
type recorder struct{ errs int }

func (r *recorder) Helper()               {}
func (r *recorder) Errorf(string, ...any) { r.errs++ }
func (r *recorder) Failed() bool          { return r.errs > 0 }

func TestCheckFailureFields(t *testing.T) {
	allow := map[string]string{
		"diagtest.badFailure.Failure": "test",
		"diagtest.embedded.Failed":    "test",
		"diagtest.row.Errors":         "test",
	}
	var ok recorder
	CheckFailureFields(&ok, allow, reflect.TypeFor[doc]())
	if ok.Failed() {
		t.Error("a full allow list must pass")
	}

	var missing recorder
	CheckFailureFields(&missing, map[string]string{}, reflect.TypeFor[doc]())
	if !missing.Failed() {
		t.Error("an offender outside the allow list must fail")
	}

	var stale recorder
	allow["diagtest.row.Name"] = "test"
	CheckFailureFields(&stale, allow, reflect.TypeFor[doc]())
	if !stale.Failed() {
		t.Error("a stale allow list entry must fail")
	}
}
