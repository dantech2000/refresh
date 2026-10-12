package apidoc

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"gopkg.in/yaml.v3"
)

// sample is a document with every JSON value type that a document can hold.
type sample struct {
	Name     string            `json:"name"`
	Version  string            `json:"version"`
	Flag     string            `json:"flag"`
	Note     string            `json:"note"`
	Count    int64             `json:"count"`
	Ratio    float64           `json:"ratio"`
	Ready    bool              `json:"ready"`
	Items    []item            `json:"items"`
	Labels   map[string]string `json:"labels"`
	Failures []string          `json:"failures"`
	Skipped  []string          `json:"skipped,omitempty"`
}

type item struct {
	InstanceType string `json:"instanceType"`
	DesiredSize  int    `json:"desiredSize"`
}

func (sample) Kind() Kind { return "Sample" }

type empty struct{}

func (empty) Kind() Kind { return "Empty" }

type nilList struct {
	Items []string `json:"items"`
}

func (nilList) Kind() Kind { return "NilList" }

type nilMap struct {
	Nested struct {
		Labels map[string]string `json:"labels"`
	} `json:"nested"`
}

func (nilMap) Kind() Kind { return "NilMap" }

type nilPointer struct {
	Next *item `json:"next"`
}

func (nilPointer) Kind() Kind { return "NilPointer" }

type list []string

func (list) Kind() Kind { return "List" }

func newSample() sample {
	return sample{
		Name:     "prod",
		Version:  "1.32",
		Flag:     "true",
		Note:     "first line\nsecond: line",
		Count:    12345678901,
		Ratio:    0.5,
		Ready:    true,
		Items:    []item{{InstanceType: "m7g.large", DesiredSize: 3}},
		Labels:   map[string]string{"team": "core"},
		Failures: []string{},
	}
}

func TestEncodeJSON(t *testing.T) {
	var buf bytes.Buffer
	if err := EncodeJSON(&buf, newSample()); err != nil {
		t.Fatalf("EncodeJSON: %v", err)
	}
	want := `{
  "apiVersion": "refresh.drod.dev/v2",
  "kind": "Sample",
  "name": "prod",
  "version": "1.32",
  "flag": "true",
  "note": "first line\nsecond: line",
  "count": 12345678901,
  "ratio": 0.5,
  "ready": true,
  "items": [
    {
      "instanceType": "m7g.large",
      "desiredSize": 3
    }
  ],
  "labels": {
    "team": "core"
  },
  "failures": []
}
`
	if diff := cmp.Diff(want, buf.String()); diff != "" {
		t.Errorf("EncodeJSON output (-want +got):\n%s", diff)
	}
}

func TestEncodeYAML(t *testing.T) {
	var buf bytes.Buffer
	if err := EncodeYAML(&buf, newSample()); err != nil {
		t.Fatalf("EncodeYAML: %v", err)
	}
	want := `apiVersion: refresh.drod.dev/v2
kind: Sample
name: prod
version: "1.32"
flag: "true"
note: |-
  first line
  second: line
count: 12345678901
ratio: 0.5
ready: true
items:
  - instanceType: m7g.large
    desiredSize: 3
labels:
  team: core
failures: []
`
	if diff := cmp.Diff(want, buf.String()); diff != "" {
		t.Errorf("EncodeYAML output (-want +got):\n%s", diff)
	}
}

// TestEncodingsAgree checks that the YAML form carries the same data as the
// JSON form for strings that YAML could read as another type.
func TestEncodingsAgree(t *testing.T) {
	doc := newSample()
	doc.Labels = map[string]string{
		"null": "null", "no": "no", "tilde": "~", "empty": "", "number": "010",
		"date": "2026-01-02", "colon": "a: b", "hash": "a #b", "html": "<a&b>",
		"quote": `say "hi"`, "unicode": "café  ", "space": " padded ",
	}

	var jsonBuf, yamlBuf bytes.Buffer
	if err := EncodeJSON(&jsonBuf, doc); err != nil {
		t.Fatalf("EncodeJSON: %v", err)
	}
	if err := EncodeYAML(&yamlBuf, doc); err != nil {
		t.Fatalf("EncodeYAML: %v", err)
	}
	var fromJSON, fromYAML any
	if err := json.Unmarshal(jsonBuf.Bytes(), &fromJSON); err != nil {
		t.Fatalf("reading the JSON output: %v", err)
	}
	if err := yaml.Unmarshal(yamlBuf.Bytes(), &fromYAML); err != nil {
		t.Fatalf("reading the YAML output: %v", err)
	}
	// YAML reads whole numbers as int and JSON reads every number as
	// float64. Compare through JSON to give both the same number type.
	normalized, err := json.Marshal(fromYAML)
	if err != nil {
		t.Fatalf("normalizing the YAML output: %v", err)
	}
	var got any
	if err := json.Unmarshal(normalized, &got); err != nil {
		t.Fatalf("normalizing the YAML output: %v", err)
	}
	if diff := cmp.Diff(fromJSON, got); diff != "" {
		t.Errorf("YAML differs from JSON (-json +yaml):\n%s\nYAML output:\n%s", diff, yamlBuf.String())
	}
}

func TestEncodeEmptyDocument(t *testing.T) {
	tests := []struct {
		name   string
		encode func(*bytes.Buffer) error
		want   string
	}{
		{
			name:   "json",
			encode: func(b *bytes.Buffer) error { return EncodeJSON(b, empty{}) },
			want:   "{\n  \"apiVersion\": \"refresh.drod.dev/v2\",\n  \"kind\": \"Empty\"\n}\n",
		},
		{
			name:   "yaml",
			encode: func(b *bytes.Buffer) error { return EncodeYAML(b, empty{}) },
			want:   "apiVersion: refresh.drod.dev/v2\nkind: Empty\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			if err := tt.encode(&buf); err != nil {
				t.Fatalf("encode: %v", err)
			}
			if diff := cmp.Diff(tt.want, buf.String()); diff != "" {
				t.Errorf("output (-want +got):\n%s", diff)
			}
		})
	}
}

func TestEncodeRejectsInvalidDocuments(t *testing.T) {
	tests := []struct {
		name    string
		doc     Document
		wantErr string
	}{
		{name: "nil list", doc: nilList{}, wantErr: `null value near "items"`},
		{name: "nil map", doc: nilMap{}, wantErr: `null value near "labels"`},
		{name: "nil pointer", doc: nilPointer{}, wantErr: `null value near "next"`},
		{name: "not an object", doc: list{"a"}, wantErr: "does not encode as a JSON object"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for format, encode := range map[string]func(*bytes.Buffer, Document) error{
				"json": func(b *bytes.Buffer, d Document) error { return EncodeJSON(b, d) },
				"yaml": func(b *bytes.Buffer, d Document) error { return EncodeYAML(b, d) },
			} {
				var buf bytes.Buffer
				err := encode(&buf, tt.doc)
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf("%s: error = %v, want one that contains %q", format, err, tt.wantErr)
				}
				if buf.Len() != 0 {
					t.Errorf("%s: wrote %q before failing, want nothing", format, buf.String())
				}
			}
		})
	}
}

func TestKinds(t *testing.T) {
	seen := map[Kind]bool{}
	for _, k := range Kinds() {
		if k == "" {
			t.Error("Kinds() contains an empty kind")
		}
		if seen[k] {
			t.Errorf("Kinds() lists %q twice", k)
		}
		seen[k] = true
	}
	if got := (Version{}).Kind(); !seen[got] {
		t.Errorf("Kinds() does not list %q", got)
	}
}
