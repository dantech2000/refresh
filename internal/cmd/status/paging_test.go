package status_test

import (
	"testing"

	"github.com/dantech2000/refresh/internal/cmd/cmdtest"
)

// TestReadsArePageable runs status with every AWS list answered one item
// per page, and compares with the unpaged run.
func TestReadsArePageable(t *testing.T) {
	cmdtest.SamePaged(t, newFake, "status", "-o", "json")
}
