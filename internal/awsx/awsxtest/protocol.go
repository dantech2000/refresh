package awsxtest

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/aws/smithy-go/encoding/cbor"
)

// protocol is one AWS wire protocol, seen from the server.
type protocol interface {
	// name is the protocol's Smithy name.
	name() string
	// decode returns the operation name and the input of a request. An
	// operation the service does not have still gets a name, for the
	// request log.
	decode(r *http.Request, body []byte) (action string, in input, err error)
	writeResult(w http.ResponseWriter, action string, result any)
	writeError(w http.ResponseWriter, e *apiError)
}

// route maps an HTTP method and path to a REST operation. A path segment in
// braces is a parameter, stored in the input under that name.
type route struct {
	method, path, action string
}

// restJSON is the restJson1 protocol.
type restJSON struct {
	routes []route
}

func (restJSON) name() string { return "restJson1" }

func (p restJSON) decode(r *http.Request, body []byte) (string, input, error) {
	in := input{}
	if len(body) > 0 {
		if err := json.Unmarshal(body, &in); err != nil {
			return "", nil, fmt.Errorf("decoding the JSON body: %w", err)
		}
	}
	for key, values := range r.URL.Query() {
		if len(values) == 1 {
			in[key] = values[0]
			continue
		}
		list := make([]any, len(values))
		for i, v := range values {
			list[i] = v
		}
		in[key] = list
	}
	segments := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	for _, rt := range p.routes {
		if rt.method != r.Method {
			continue
		}
		if params, ok := matchPath(rt.path, segments); ok {
			for k, v := range params {
				in[k] = v
			}
			return rt.action, in, nil
		}
	}
	return r.Method + " " + r.URL.Path, in, nil
}

func matchPath(pattern string, segments []string) (map[string]string, bool) {
	want := strings.Split(strings.Trim(pattern, "/"), "/")
	if len(want) != len(segments) {
		return nil, false
	}
	params := map[string]string{}
	for i, seg := range want {
		if name, ok := strings.CutPrefix(seg, "{"); ok {
			params[strings.TrimSuffix(name, "}")] = segments[i]
		} else if seg != segments[i] {
			return nil, false
		}
	}
	return params, true
}

func (restJSON) writeResult(w http.ResponseWriter, _ string, result any) {
	writeJSON(w, "application/json", http.StatusOK, result)
}

func (restJSON) writeError(w http.ResponseWriter, e *apiError) {
	w.Header().Set("X-Amzn-Errortype", e.code)
	writeJSON(w, "application/json", e.status, map[string]string{"message": e.message})
}

// awsJSON is the awsJson1_1 protocol.
type awsJSON struct {
	// targetPrefix is what precedes the operation name in X-Amz-Target.
	targetPrefix string
}

func (awsJSON) name() string { return "awsJson1_1" }

func (p awsJSON) decode(r *http.Request, body []byte) (string, input, error) {
	target := r.Header.Get("X-Amz-Target")
	action, ok := strings.CutPrefix(target, p.targetPrefix)
	if !ok {
		return "", nil, fmt.Errorf("unexpected X-Amz-Target %q", target)
	}
	in := input{}
	if err := json.Unmarshal(body, &in); err != nil {
		return "", nil, fmt.Errorf("decoding the JSON body: %w", err)
	}
	return action, in, nil
}

func (awsJSON) writeResult(w http.ResponseWriter, _ string, result any) {
	writeJSON(w, "application/x-amz-json-1.1", http.StatusOK, result)
}

func (awsJSON) writeError(w http.ResponseWriter, e *apiError) {
	writeJSON(w, "application/x-amz-json-1.1", e.status, map[string]string{"__type": e.code, "message": e.message})
}

func writeJSON(w http.ResponseWriter, contentType string, status int, v any) {
	w.Header().Set("Content-Type", contentType)
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v) // a write error means the client left
}

// awsQuery is the awsQuery protocol: a form in, XML out.
type awsQuery struct {
	namespace string
}

func (awsQuery) name() string { return "awsQuery" }

func (awsQuery) decode(_ *http.Request, body []byte) (string, input, error) {
	return decodeForm(body)
}

func (p awsQuery) writeResult(w http.ResponseWriter, action string, result any) {
	kids, _ := result.([]node)
	writeXML(w, http.StatusOK, node{name: action + "Response", namespace: p.namespace, kids: []node{
		tag(action+"Result", kids...),
		tag("ResponseMetadata", leaf("RequestId", requestID)),
	}})
}

func (p awsQuery) writeError(w http.ResponseWriter, e *apiError) {
	writeXML(w, e.status, node{name: "ErrorResponse", namespace: p.namespace, kids: []node{
		tag("Error", leaf("Type", "Sender"), leaf("Code", e.code), leaf("Message", e.message)),
		leaf("RequestId", requestID),
	}})
}

// ec2Query is the ec2Query protocol. It differs from awsQuery in the
// response and error envelopes and in how lists are named.
type ec2Query struct{}

func (ec2Query) name() string { return "ec2Query" }

func (ec2Query) decode(_ *http.Request, body []byte) (string, input, error) {
	return decodeForm(body)
}

func (ec2Query) writeResult(w http.ResponseWriter, action string, result any) {
	kids, _ := result.([]node)
	writeXML(w, http.StatusOK, node{
		name: action + "Response", namespace: "http://ec2.amazonaws.com/doc/2016-11-15/",
		kids: append([]node{leaf("requestId", requestID)}, kids...),
	})
}

func (ec2Query) writeError(w http.ResponseWriter, e *apiError) {
	writeXML(w, e.status, tag("Response",
		tag("Errors", tag("Error", leaf("Code", e.code), leaf("Message", e.message))),
		leaf("RequestID", requestID),
	))
}

func decodeForm(body []byte) (string, input, error) {
	form, err := url.ParseQuery(string(body))
	if err != nil {
		return "", nil, fmt.Errorf("decoding the form body: %w", err)
	}
	in := input{}
	for key, values := range form {
		if key != "Action" && key != "Version" && len(values) > 0 {
			in[key] = values[0]
		}
	}
	return form.Get("Action"), in, nil
}

const requestID = "00000000-0000-4000-8000-000000000000"

// node is an XML element: text, or child elements.
type node struct {
	name      string
	namespace string
	text      string
	kids      []node
}

func tag(name string, kids ...node) node { return node{name: name, kids: kids} }

// leaf returns a text element, or nothing when the text is empty: AWS
// leaves an unset member out.
func leaf(name, text string) node {
	if text == "" {
		return node{}
	}
	return node{name: name, text: text}
}

// each returns one element per item, for the members of a list.
func each[T any](items []T, f func(T) node) []node {
	out := make([]node, 0, len(items))
	for _, it := range items {
		out = append(out, f(it))
	}
	return out
}

func (n node) write(b *bytes.Buffer) {
	if n.name == "" {
		return
	}
	b.WriteString("<" + n.name)
	if n.namespace != "" {
		b.WriteString(` xmlns="` + n.namespace + `"`)
	}
	b.WriteString(">")
	_ = xml.EscapeText(b, []byte(n.text)) // a bytes.Buffer does not fail
	for _, k := range n.kids {
		k.write(b)
	}
	b.WriteString("</" + n.name + ">")
}

func writeXML(w http.ResponseWriter, status int, root node) {
	var b bytes.Buffer
	root.write(&b)
	w.Header().Set("Content-Type", "text/xml")
	w.WriteHeader(status)
	_, _ = w.Write(b.Bytes()) // a write error means the client left
}

func xmlTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// rpcCBOR is the Smithy RPC v2 CBOR protocol.
type rpcCBOR struct{}

func (rpcCBOR) name() string { return "rpcv2Cbor" }

func (rpcCBOR) decode(r *http.Request, body []byte) (string, input, error) {
	// The path is /service/{service}/operation/{operation}.
	segments := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(segments) != 4 || segments[0] != "service" || segments[2] != "operation" {
		return "", nil, fmt.Errorf("unexpected RPC v2 path %q", r.URL.Path)
	}
	if got := r.Header.Get("Smithy-Protocol"); got != "rpc-v2-cbor" {
		return "", nil, fmt.Errorf("unexpected Smithy-Protocol %q", got)
	}
	in := input{}
	if len(body) > 0 {
		v, err := cbor.Decode(body)
		if err != nil {
			return "", nil, fmt.Errorf("decoding the CBOR body: %w", err)
		}
		m, ok := fromCBOR(v).(map[string]any)
		if !ok {
			return "", nil, errors.New("the CBOR body is not a map")
		}
		in = m
	}
	return segments[3], in, nil
}

func (rpcCBOR) writeResult(w http.ResponseWriter, _ string, result any) {
	m, _ := result.(cbor.Map)
	writeCBOR(w, http.StatusOK, m)
}

func (rpcCBOR) writeError(w http.ResponseWriter, e *apiError) {
	writeCBOR(w, e.status, cbor.Map{"__type": cbor.String(e.code), "message": cbor.String(e.message)})
}

func writeCBOR(w http.ResponseWriter, status int, m cbor.Map) {
	w.Header().Set("Smithy-Protocol", "rpc-v2-cbor")
	w.Header().Set("Content-Type", "application/cbor")
	w.WriteHeader(status)
	_, _ = w.Write(cbor.Encode(m)) // a write error means the client left
}

// fromCBOR converts a CBOR value to the types Request.Input documents.
func fromCBOR(v cbor.Value) any {
	switch v := v.(type) {
	case cbor.Map:
		out := make(map[string]any, len(v))
		for k, e := range v {
			out[k] = fromCBOR(e)
		}
		return out
	case cbor.List:
		out := make([]any, len(v))
		for i, e := range v {
			out[i] = fromCBOR(e)
		}
		return out
	case cbor.String:
		return string(v)
	case cbor.Bool:
		return bool(v)
	case cbor.Slice:
		return []byte(v)
	case *cbor.Tag:
		if t, err := cbor.AsTime(v); err == nil {
			return t.UTC()
		}
		return fromCBOR(v.Value)
	}
	if f, err := cbor.AsFloat64(v); err == nil {
		return f
	}
	if n, err := cbor.AsInt64(v); err == nil {
		return float64(n)
	}
	return nil
}

func cborTime(t time.Time) cbor.Value {
	return &cbor.Tag{ID: 1, Value: cbor.Float64(float64(t.UnixMilli()) / 1e3)}
}
