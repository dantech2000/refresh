package apidoc

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"gopkg.in/yaml.v3"
)

// EncodeJSON writes doc to w as indented JSON. The first two keys are
// apiVersion and kind; the rest follow in the order of the document's fields.
func EncodeJSON(w io.Writer, doc Document) error {
	compact, err := marshal(doc)
	if err != nil {
		return err
	}
	var buf bytes.Buffer
	if err := json.Indent(&buf, compact, "", "  "); err != nil {
		return err
	}
	buf.WriteByte('\n')
	_, err = w.Write(buf.Bytes())
	return err
}

// EncodeYAML writes doc to w as YAML with the same keys, in the same order,
// as EncodeJSON. The document goes through its JSON form, so the json struct
// tags name the keys of both formats.
func EncodeYAML(w io.Writer, doc Document) error {
	compact, err := marshal(doc)
	if err != nil {
		return err
	}
	// JSON is YAML, so the YAML parser reads it into a node tree that keeps
	// the key order and the string tags. Clearing the styles turns the JSON
	// flow layout into block YAML.
	var node yaml.Node
	if err := yaml.Unmarshal(compact, &node); err != nil {
		return fmt.Errorf("converting the %s document to YAML: %w", doc.Kind(), err)
	}
	clearStyle(&node)

	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&node); err != nil {
		return err
	}
	if err := enc.Close(); err != nil {
		return err
	}
	_, err = w.Write(buf.Bytes())
	return err
}

// header is the two keys that start every document.
type header struct {
	APIVersion string `json:"apiVersion"`
	Kind       Kind   `json:"kind"`
}

// marshal returns doc as compact JSON that starts with apiVersion and kind.
// It fails when doc is not a JSON object or contains a null.
func marshal(doc Document) ([]byte, error) {
	body, err := json.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("encoding the %s document: %w", doc.Kind(), err)
	}
	if len(body) < 2 || body[0] != '{' {
		return nil, fmt.Errorf("the %s document does not encode as a JSON object", doc.Kind())
	}
	if err := checkNoNull(body); err != nil {
		return nil, fmt.Errorf("the %s document: %w", doc.Kind(), err)
	}
	out, err := json.Marshal(header{APIVersion: APIVersion, Kind: doc.Kind()})
	if err != nil {
		return nil, err
	}
	out = out[:len(out)-1] // drop the closing brace
	if len(body) > 2 {
		out = append(out, ',')
	}
	return append(out, body[1:]...), nil
}

// checkNoNull returns an error when the JSON in data contains a null. A nil
// slice or map in a document type is a bug in that type: the field needs an
// empty value or omitempty.
func checkNoNull(data []byte) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	var lastKey string
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		switch v := tok.(type) {
		case nil:
			return fmt.Errorf("null value near %q; use an empty list or map, or omit the key", lastKey)
		case string:
			lastKey = v
		}
	}
}

// clearStyle removes the flow and quoting styles from n and its children.
// The encoder then chooses block layout, and quotes a string only when YAML
// needs it.
func clearStyle(n *yaml.Node) {
	n.Style = 0
	for _, c := range n.Content {
		clearStyle(c)
	}
}
