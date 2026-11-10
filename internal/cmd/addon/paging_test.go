package addon_test

import (
	"testing"

	"github.com/dantech2000/refresh/internal/cmd/cmdtest"
)

// TestReadsArePageable runs addon list with every AWS list answered one
// item per page, and compares with the unpaged run. addon describe reads
// no list with two items, so it has no case.
func TestReadsArePageable(t *testing.T) {
	cmdtest.SamePaged(t, newFake, "addon", "list", "-c", "prod", "-o", "json")
}
