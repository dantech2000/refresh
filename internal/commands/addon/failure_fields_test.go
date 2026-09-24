package addon

import (
	"reflect"
	"testing"

	"github.com/dantech2000/refresh/internal/diag/diagtest"
)

// The add-on update documents report failures only as diag.Failure values
// (docs/concepts/output.md).
func TestUpdateDocumentsReportFailuresWithDiag(t *testing.T) {
	diagtest.CheckFailureFields(t, map[string]string{},
		reflect.TypeFor[addonUpdateDocument](),
		reflect.TypeFor[addonUpdateAllDocument](),
	)
}
