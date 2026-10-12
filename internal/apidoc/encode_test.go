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

type ownKind struct {
	Name string `json:"name"`
	Type string `json:"kind"`
}

func (ownKind) Kind() Kind { return "OwnKind" }

type ownAPIVersion struct {
	APIVersion string `json:"apiVersion"`
}

func (ownAPIVersion) Kind() Kind { return "OwnAPIVersion" }

// nestedKind has a kind key below the top level, which is allowed.
type nestedKind struct {
	Target struct {
		Kind       string `json:"kind"`
		APIVersion string `json:"apiVersion"`
	} `json:"target"`
}

func (nestedKind) Kind() Kind { return "NestedKind" }

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

// numbers is a document with numbers that float64 cannot hold exactly.
type numbers struct {
	MaxUint  uint64      `json:"maxUint"`
	MinInt   int64       `json:"minInt"`
	Above53  int64       `json:"above53"`
	Huge     json.Number `json:"huge"`
	Exponent json.Number `json:"exponent"`
	Tiny     float64     `json:"tiny"`
	Large    float64     `json:"large"`
}

func (numbers) Kind() Kind { return "Numbers" }

func newNumbers() numbers {
	return numbers{
		MaxUint:  18446744073709551615,
		MinInt:   -9223372036854775808,
		Above53:  9007199254740993, // 2^53 + 1
		Huge:     "123456789012345678901234567890",
		Exponent: "1.5e+300",
		Tiny:     1e-7,
		Large:    1e21,
	}
}

func TestEncodeKeepsNumbersExact(t *testing.T) {
	var jsonBuf, yamlBuf bytes.Buffer
	if err := EncodeJSON(&jsonBuf, newNumbers()); err != nil {
		t.Fatalf("EncodeJSON: %v", err)
	}
	if err := EncodeYAML(&yamlBuf, newNumbers()); err != nil {
		t.Fatalf("EncodeYAML: %v", err)
	}
	wantJSON := `{
  "apiVersion": "refresh.drod.dev/v2",
  "kind": "Numbers",
  "maxUint": 18446744073709551615,
  "minInt": -9223372036854775808,
  "above53": 9007199254740993,
  "huge": 123456789012345678901234567890,
  "exponent": 1.5e+300,
  "tiny": 1e-7,
  "large": 1e+21
}
`
	wantYAML := `apiVersion: refresh.drod.dev/v2
kind: Numbers
maxUint: 18446744073709551615
minInt: -9223372036854775808
above53: 9007199254740993
huge: 123456789012345678901234567890
exponent: 1.5e+300
tiny: 1e-7
large: 1e+21
`
	if diff := cmp.Diff(wantJSON, jsonBuf.String()); diff != "" {
		t.Errorf("EncodeJSON output (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff(wantYAML, yamlBuf.String()); diff != "" {
		t.Errorf("EncodeYAML output (-want +got):\n%s", diff)
	}
}

// outOfRange holds a number that is valid JSON and more than float64 holds.
type outOfRange struct {
	Value json.Number `json:"value"`
}

func (outOfRange) Kind() Kind { return "OutOfRange" }

// TestEncodeNumberOutOfFloatRange: the encoder does not convert a number, so
// one that float64 cannot hold passes through with its text unchanged. It is
// not in TestEncodingsAgree because yaml.v3 reads this YAML number back as a
// string; the bytes are what this test checks.
func TestEncodeNumberOutOfFloatRange(t *testing.T) {
	doc := outOfRange{Value: "1e400"}
	var jsonBuf, yamlBuf bytes.Buffer
	if err := EncodeJSON(&jsonBuf, doc); err != nil {
		t.Fatalf("EncodeJSON: %v", err)
	}
	if err := EncodeYAML(&yamlBuf, doc); err != nil {
		t.Fatalf("EncodeYAML: %v", err)
	}
	wantJSON := "{\n  \"apiVersion\": \"refresh.drod.dev/v2\",\n  \"kind\": \"OutOfRange\",\n  \"value\": 1e400\n}\n"
	if diff := cmp.Diff(wantJSON, jsonBuf.String()); diff != "" {
		t.Errorf("EncodeJSON output (-want +got):\n%s", diff)
	}
	wantYAML := "apiVersion: refresh.drod.dev/v2\nkind: OutOfRange\nvalue: 1e400\n"
	if diff := cmp.Diff(wantYAML, yamlBuf.String()); diff != "" {
		t.Errorf("EncodeYAML output (-want +got):\n%s", diff)
	}
}

// TestEncodingsAgree checks that the YAML form carries the same data as the
// JSON form: strings that YAML could read as another type stay strings, and
// every number has the same digits in both.
func TestEncodingsAgree(t *testing.T) {
	text := newSample()
	text.Labels = map[string]string{
		"null": "null", "no": "no", "tilde": "~", "empty": "", "number": "010",
		"date": "2026-01-02", "colon": "a: b", "hash": "a #b", "html": "<a&b>",
		"quote": `say "hi"`, "unicode": "caf\u00e9 \u2028", "space": " padded ",
		"big": "18446744073709551615", "exponent": "1e400",
	}
	for name, doc := range map[string]Document{"text": text, "numbers": newNumbers()} {
		t.Run(name, func(t *testing.T) {
			var jsonBuf, yamlBuf bytes.Buffer
			if err := EncodeJSON(&jsonBuf, doc); err != nil {
				t.Fatalf("EncodeJSON: %v", err)
			}
			if err := EncodeYAML(&yamlBuf, doc); err != nil {
				t.Fatalf("EncodeYAML: %v", err)
			}

			// Both sides keep a number as its text (json.Number), so the
			// comparison sees a digit that a float64 would round away.
			dec := json.NewDecoder(&jsonBuf)
			dec.UseNumber()
			var fromJSON any
			if err := dec.Decode(&fromJSON); err != nil {
				t.Fatalf("reading the JSON output: %v", err)
			}
			var node yaml.Node
			if err := yaml.Unmarshal(yamlBuf.Bytes(), &node); err != nil {
				t.Fatalf("reading the YAML output: %v", err)
			}
			fromYAML := yamlValue(t, &node)

			if diff := cmp.Diff(fromJSON, fromYAML); diff != "" {
				t.Errorf("YAML differs from JSON (-json +yaml):\n%s\nYAML output:\n%s", diff, yamlBuf.String())
			}
		})
	}
}

// yamlValue converts a parsed YAML node to the value that encoding/json
// gives for the same data with UseNumber: a number is a json.Number that
// holds the text of the scalar.
func yamlValue(t *testing.T, n *yaml.Node) any {
	t.Helper()
	switch n.Kind {
	case yaml.DocumentNode:
		return yamlValue(t, n.Content[0])
	case yaml.MappingNode:
		m := map[string]any{}
		for i := 0; i < len(n.Content); i += 2 {
			m[n.Content[i].Value] = yamlValue(t, n.Content[i+1])
		}
		return m
	case yaml.SequenceNode:
		s := []any{}
		for _, c := range n.Content {
			s = append(s, yamlValue(t, c))
		}
		return s
	}
	switch n.Tag {
	case "!!str":
		return n.Value
	case "!!bool":
		return n.Value == "true"
	case "!!int", "!!float":
		return json.Number(n.Value)
	}
	t.Fatalf("YAML scalar %q has tag %s, want a string, a boolean, or a number", n.Value, n.Tag)
	return nil
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
		{name: "own kind key", doc: ownKind{Name: "prod", Type: "Cluster"}, wantErr: `the OwnKind document has its own "kind" key`},
		{name: "own apiVersion key", doc: ownAPIVersion{APIVersion: "v1"}, wantErr: `the OwnAPIVersion document has its own "apiVersion" key`},
		{name: "empty own apiVersion key", doc: ownAPIVersion{}, wantErr: `the OwnAPIVersion document has its own "apiVersion" key`},
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

func TestEncodeAllowsNestedReservedKeys(t *testing.T) {
	doc := nestedKind{}
	doc.Target.Kind, doc.Target.APIVersion = "Deployment", "apps/v1"
	var buf bytes.Buffer
	if err := EncodeYAML(&buf, doc); err != nil {
		t.Fatalf("EncodeYAML: %v", err)
	}
	want := "apiVersion: refresh.drod.dev/v2\nkind: NestedKind\ntarget:\n  kind: Deployment\n  apiVersion: apps/v1\n"
	if diff := cmp.Diff(want, buf.String()); diff != "" {
		t.Errorf("EncodeYAML output (-want +got):\n%s", diff)
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
