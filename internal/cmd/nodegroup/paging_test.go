package nodegroup_test

import (
	"testing"

	"github.com/dantech2000/refresh/internal/cmd/cmdtest"
)

// TestReadsArePageable runs nodegroup list with every AWS list answered
// one item per page, and compares with the unpaged run. nodegroup describe
// reads no list with two items, so it has no case.
func TestReadsArePageable(t *testing.T) {
	cmdtest.SamePaged(t, newFake, "nodegroup", "list", "-c", "prod", "-o", "json")
}
