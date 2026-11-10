package cluster_test

import (
	"testing"

	"github.com/dantech2000/refresh/internal/cmd/cmdtest"
)

// TestReadsArePageable runs each read of the group with every AWS list
// answered one item per page, and compares with the unpaged run.
func TestReadsArePageable(t *testing.T) {
	check := func(t *testing.T) *cmdtest.Fake {
		t.Helper()
		return newCheckFake(t, nil)
	}
	list := func(t *testing.T) *cmdtest.Fake {
		t.Helper()
		return newEnv(t, newWorld()).Fake
	}
	for _, tt := range []struct {
		name string
		fake func(t *testing.T) *cmdtest.Fake
		args []string
	}{
		{"list", list, []string{"cluster", "list", "-o", "json"}},
		{"describe", newDescribeFake, []string{"cluster", "describe", "prod", "-o", "json"}},
		{"upgrade-check", check, []string{"cluster", "upgrade-check", "prod", "-o", "json"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cmdtest.SamePaged(t, tt.fake, tt.args...)
		})
	}
}
