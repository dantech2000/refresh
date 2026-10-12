package awsxtest

import (
	"cmp"
	"fmt"
	"slices"
	"strconv"
)

// input is a decoded request. See Request.Input for its shape.
type input map[string]any

// str returns the string at key, or "".
func (in input) str(key string) string {
	s, _ := in[key].(string)
	return s
}

// strs returns the strings at key: a JSON or CBOR list, or a single value.
func (in input) strs(key string) []string {
	switch v := in[key].(type) {
	case string:
		return []string{v}
	case []any:
		out := make([]string, 0, len(v))
		for _, e := range v {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// num returns the integer at key, which is a number or a decimal string.
func (in input) num(key string) (int, bool) {
	switch v := in[key].(type) {
	case float64:
		return int(v), true
	case string:
		n, err := strconv.Atoi(v)
		return n, err == nil
	}
	return 0, false
}

// obj returns the object at key, or an empty one.
func (in input) obj(key string) input {
	m, _ := in[key].(map[string]any)
	return m
}

// objs returns the objects in the list at key.
func (in input) objs(key string) []input {
	list, _ := in[key].([]any)
	out := make([]input, 0, len(list))
	for _, e := range list {
		if m, ok := e.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

// list returns the values of a query-protocol list: prefix.1, prefix.2, and
// so on.
func (in input) list(prefix string) []string {
	var out []string
	for i := 1; ; i++ {
		v, ok := in[prefix+"."+strconv.Itoa(i)].(string)
		if !ok {
			return out
		}
		out = append(out, v)
	}
}

// clusterName returns the cluster an EKS request names. The EKS model calls
// it "name" in some operations and "clusterName" in others.
func (in input) clusterName() string {
	return cmp.Or(in.str("clusterName"), in.str("name"))
}

// cloneInput returns a copy of in that shares no memory with it.
func cloneInput(in input) input {
	out, _ := cloneValue(map[string]any(in)).(map[string]any)
	return out
}

func cloneValue(v any) any {
	switch v := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(v))
		for k, e := range v {
			out[k] = cloneValue(e)
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, e := range v {
			out[i] = cloneValue(e)
		}
		return out
	case []byte:
		return slices.Clone(v)
	}
	return v // strings, numbers, booleans, and times are immutable
}

// page returns one page of items and the token of the next page, or "" on
// the last. A fault's page size wins over the request's limit, which is
// read from limitKey.
func page[T any](c *call, items []T, limitKey, tokenKey string) ([]T, string, error) {
	start := 0
	if token := c.in.str(tokenKey); token != "" {
		n, err := strconv.Atoi(token)
		if err != nil || n < 0 || n > len(items) {
			return nil, "", newError(c.svc.badToken, fmt.Sprintf("invalid %s %q", tokenKey, token))
		}
		start = n
	}
	size := c.pageSize
	if size <= 0 {
		size, _ = c.in.num(limitKey)
	}
	end := len(items)
	if size > 0 && start+size < end {
		end = start + size
	}
	next := ""
	if end < len(items) {
		next = strconv.Itoa(end)
	}
	return items[start:end], next, nil
}
