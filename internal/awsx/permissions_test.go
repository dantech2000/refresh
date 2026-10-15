package awsx

import (
	"errors"
	"strings"
	"testing"
)

// The text of a row names commands and flags of the v2 contract only.
func TestRequiredPermissionsUseContractNames(t *testing.T) {
	removed := []string{"--wait`", "--detailed", "--health-only", "refresh ui", "partial", "--id", "install-man", "--tree"}
	for _, p := range RequiredPermissions() {
		for _, old := range removed {
			if strings.Contains(p.UsedBy, old) {
				t.Errorf("%v: the text %q names %q, which the v2 contract does not have", p.Actions, p.UsedBy, old)
			}
		}
	}
}

func TestPermissionErrorListsEveryAction(t *testing.T) {
	msg := formatPermissionError(errors.New("denied"), "listing clusters").Error()
	for _, p := range RequiredPermissions() {
		for _, a := range p.Actions {
			if !strings.Contains(msg, a) {
				t.Errorf("the hint does not name %s", a)
			}
		}
	}
	if !strings.Contains(msg, PermissionsDocURL) {
		t.Error("the hint does not link the docs table")
	}
	if strings.Contains(msg, "`") {
		t.Error("the hint contains Markdown backticks")
	}
}

func TestRequiredPermissionsListsEachActionOnce(t *testing.T) {
	seen := map[string]bool{}
	for _, p := range RequiredPermissions() {
		for _, a := range p.Actions {
			if seen[a] {
				t.Errorf("%s is listed twice", a)
			}
			seen[a] = true
		}
	}
}
