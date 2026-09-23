package runner

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	clustersvc "github.com/dantech2000/refresh/internal/services/cluster"
	statussvc "github.com/dantech2000/refresh/internal/services/status"
)

type inner struct {
	Items []string `json:"items"`
}

type embedded struct {
	Failed []string `json:"failed"`
}

type outer struct {
	embedded
	Names   []string          `json:"names"`
	Skip    []string          `json:"skip,omitempty"`
	Inner   inner             `json:"inner"`
	Ptr     *inner            `json:"ptr"`
	List    []inner           `json:"list"`
	Raw     json.RawMessage   `json:"raw,omitempty"`
	When    time.Time         `json:"when"`
	ByName  map[string]inner  `json:"byName"`
	Any     any               `json:"any"`
	private []string          //nolint:unused // proves unexported fields are left alone
	Tags    map[string]string `json:"tags"`
}

func encodeJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(emptySlices(v))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// Every empty list in a machine payload is `[]`, never `null`.
func TestEmptySlices_EmptyPayloads(t *testing.T) {
	for _, tc := range []struct {
		name    string
		payload any
		want    string
	}{
		{"status fleet", statussvc.FleetStatus{}, `"clusters":[]`},
		{"cluster list", map[string]any{"clusters": []clustersvc.ClusterSummary(nil), "count": 0}, `"clusters":[]`},
		{"cluster describe", &clustersvc.ClusterDetails{Name: "prod"}, `"addons":[],"nodegroups":[]`},
		{"nested", outer{}, `"failed":[],"names":[]`},
		{"nested inner", outer{}, `"inner":{"items":[]}`},
		{"pointer", outer{Ptr: &inner{}}, `"ptr":{"items":[]}`},
		{"slice elements", outer{List: []inner{{}}}, `"list":[{"items":[]}]`},
		{"map values", outer{ByName: map[string]inner{"a": {}}}, `"byName":{"a":{"items":[]}}`},
		{"interface", outer{Any: inner{}}, `"any":{"items":[]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := encodeJSON(t, tc.payload); !strings.Contains(got, tc.want) {
				t.Errorf("got %s, want it to contain %s", got, tc.want)
			}
		})
	}
}

// omitempty fields stay omitted, nil maps and pointers stay null, values
// with their own encoding are untouched, and the caller's value is not
// modified.
func TestEmptySlices_KeepsOtherShapes(t *testing.T) {
	when := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	in := outer{When: when}
	got := encodeJSON(t, in)
	for _, want := range []string{`"ptr":null`, `"tags":null`, `"byName":null`, `"any":null`, `"when":"2026-01-02T03:04:05Z"`} {
		if !strings.Contains(got, want) {
			t.Errorf("got %s, want it to contain %s", got, want)
		}
	}
	for _, absent := range []string{`"skip"`, `"raw"`} {
		if strings.Contains(got, absent) {
			t.Errorf("omitempty field %s was emitted: %s", absent, got)
		}
	}
	if in.Names != nil || in.Failed != nil {
		t.Error("emptySlices modified the caller's value")
	}
	if emptySlices(nil) != nil {
		t.Error("emptySlices(nil) != nil")
	}
}
