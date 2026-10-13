package change

import (
	"slices"
	"strings"
	"testing"
)

func TestParseParts(t *testing.T) {
	got, err := ParseParts([]string{"control-plane,ADDONS", " nodegroups ", "addons"})
	if want := []Part{PartControlPlane, PartAddons, PartNodegroups}; err != nil || !slices.Equal(got, want) {
		t.Fatalf("got %v, %v; want %v", got, err, want)
	}
	if got, err := ParseParts(nil); err != nil || got != nil {
		t.Fatalf("no value: got %v, %v", got, err)
	}
	if _, err := ParseParts([]string{"controlplane"}); err == nil || !strings.Contains(err.Error(), "control-plane, addons, nodegroups") {
		t.Fatalf("a word that is no part: err = %v", err)
	}
	for _, empty := range [][]string{{""}, {","}, {" , "}} {
		if _, err := ParseParts(empty); err == nil {
			t.Fatalf("%q: an empty --only means every part", empty)
		}
	}
}
