package cluster

import (
	"strings"
	"testing"
)

// validateClusterFilters must reject unsupported --filter keys instead of
// silently ignoring them (REF-1).
func TestValidateClusterFilters(t *testing.T) {
	for _, tc := range []struct {
		name    string
		filters map[string]string
		wantErr bool
	}{
		{"empty", nil, false},
		{"name", map[string]string{"name": "prod"}, false},
		{"status", map[string]string{"status": "ACTIVE"}, false},
		{"version", map[string]string{"version": "1.32"}, false},
		{"all supported", map[string]string{"name": "p", "status": "ACTIVE", "version": "1.32"}, false},
		{"unknown key", map[string]string{"staus": "ACTIVE"}, true},
		{"mixed known + unknown", map[string]string{"status": "ACTIVE", "color": "blue"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateClusterFilters(tc.filters)
			if (err != nil) != tc.wantErr {
				t.Fatalf("validateClusterFilters(%v) error = %v, wantErr %v", tc.filters, err, tc.wantErr)
			}
			if err != nil && !strings.Contains(err.Error(), "supported:") {
				t.Errorf("error %q should name the supported keys", err)
			}
		})
	}
}
