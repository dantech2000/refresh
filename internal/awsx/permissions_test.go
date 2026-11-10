package awsx

import (
	"errors"
	"strings"
	"testing"
)

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
