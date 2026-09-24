package nodegroup

import (
	"testing"

	"github.com/dantech2000/refresh/internal/diag/diagtest"
)

// failureFieldAllow lists the output fields that still report failures
// without diag.Failure. Each entry names the issue that migrates it, and
// the check fails when an entry is no longer needed, so the migrating PR
// must delete it. Every document has been migrated (REF-178, REF-179).
var failureFieldAllow = map[string]string{}

// TestOutputTypesReportFailuresWithDiag enforces the failure contract of
// docs/concepts/output.md: failures are []diag.Failure under "failures"
// (or one diag.Failure under "failure"), never strings or ad hoc structs.
func TestOutputTypesReportFailuresWithDiag(t *testing.T) {
	diagtest.CheckFailureFields(t, failureFieldAllow, outputRootTypes...)
}
