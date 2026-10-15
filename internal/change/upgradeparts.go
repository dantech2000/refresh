package change

import (
	"fmt"
	"slices"
	"strings"
)

// Part is one part of a cluster upgrade that --only selects. The values
// are the words of the flag.
type Part string

// The parts, in the order that a hop runs them.
const (
	PartControlPlane Part = "control-plane"
	PartAddons       Part = "addons"
	PartNodegroups   Part = "nodegroups"
)

// Parts returns every part, in run order.
func Parts() []Part { return []Part{PartControlPlane, PartAddons, PartNodegroups} }

// ParseParts reads the values of --only, each a part or a list of parts
// with commas. No value means every part, and returns nil. Values that
// name no part, such as "" or ",", are an error: an empty variable must
// never widen a run to every part.
func ParseParts(values []string) ([]Part, error) {
	var out []Part
	for _, v := range values {
		for f := range strings.SplitSeq(v, ",") {
			p := Part(strings.ToLower(strings.TrimSpace(f)))
			if p == "" {
				continue
			}
			if !slices.Contains(Parts(), p) {
				return nil, fmt.Errorf("--only %q: want %s", f, joinParts(Parts()))
			}
			if !slices.Contains(out, p) {
				out = append(out, p)
			}
		}
	}
	if len(values) > 0 && len(out) == 0 {
		return nil, fmt.Errorf("--only needs at least one of %s", joinParts(Parts()))
	}
	return out, nil
}

func joinParts(parts []Part) string {
	words := make([]string, len(parts))
	for i, p := range parts {
		words[i] = string(p)
	}
	return strings.Join(words, ", ")
}
