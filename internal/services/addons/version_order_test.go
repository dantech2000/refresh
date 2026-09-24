package addons

import (
	"context"
	"testing"

	"github.com/dantech2000/refresh/internal/mocks"
)

// A release outranks its prereleases, prereleases follow semver precedence,
// and an -eksbuild.N suffix is a build number, not a prerelease.
func TestCompareVersions_PrereleaseOrdering(t *testing.T) {
	tests := []struct {
		a, b string
		want int // sign of CompareVersions(a, b)
	}{
		{"v2.4.0", "v2.4.0-rc1", 1},
		{"v2.4.0-rc1", "v2.4.0-rc2", -1},
		{"v2.4.0-rc.2", "v2.4.0-rc.10", -1},
		{"v2.4.0-alpha", "v2.4.0-beta", -1},
		{"v2.4.0-alpha", "v2.4.0-alpha.1", -1},
		{"v1.0.0-1", "v1.0.0-alpha", -1}, // numeric identifiers rank below alphanumeric
		{"v2.4.0-rc1", "v2.3.9", 1},
		{"v2.4.0-rc1-eksbuild.1", "v2.4.0-eksbuild.1", -1},
		{"v1.19.0-eksbuild.1", "v1.19.0-eksbuild.2", -1},
		{"v1.19.0-eksbuild.10", "v1.19.0-eksbuild.9", 1},
		{"v1.19.0-eksbuild.1", "v1.18.9-eksbuild.9", 1},
		{"v1.19.0-eksbuild.1", "v1.19.0-rc1", 1},
		{"v1.19.0-eksbuild.1", "v1.19.0-eksbuild.1", 0},
		{"1.19.0-eksbuild.1", "v1.19.0-eksbuild.1", 0},
	}
	for _, tt := range tests {
		if got := sign(CompareVersions(tt.a, tt.b)); got != tt.want {
			t.Errorf("CompareVersions(%q, %q) sign = %d, want %d", tt.a, tt.b, got, tt.want)
		}
		if got := sign(CompareVersions(tt.b, tt.a)); got != -tt.want {
			t.Errorf("CompareVersions(%q, %q) sign = %d, want %d", tt.b, tt.a, got, -tt.want)
		}
	}
}

// "latest" is versions[0]: a release must not lose to its own release
// candidate.
func TestGetAvailableVersions_ReleaseBeatsPrerelease(t *testing.T) {
	m := mocks.NewEKSAPI().
		WithAddonVersions("example", []string{"v2.4.0-rc1", "v2.4.0", "v2.3.0"}, "1.32").
		Build()
	versions, err := NewService(m, logger()).GetAvailableVersions(context.Background(), "example", "1.32")
	if err != nil {
		t.Fatal(err)
	}
	if versions[0].Version != "v2.4.0" {
		t.Fatalf("latest = %s, want v2.4.0 (all: %+v)", versions[0].Version, versions)
	}
}
