package awsx

import (
	"errors"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

// TestRequiredPermissionsMatchLegacy reads the IAM table of the old tree as
// text, because Go's internal rule keeps this package from importing it, and
// checks that this table has the same rows in the same order.
//
// Remove this test at cutover, with the one in awsxtest that reads the same
// file. From then on the docs table is checked against this table alone.
func TestRequiredPermissionsMatchLegacy(t *testing.T) {
	const path = "../../legacy/internal/aws/awserr/permissions.go"
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the legacy table: %v", err)
	}
	row := regexp.MustCompile(`(?m)^\t\{\[\]string\{(.*)\}, ("(?:[^"\\]|\\.)*")\},$`)
	var legacy []Permission
	for _, m := range row.FindAllStringSubmatch(string(src), -1) {
		var actions []string
		for a := range strings.SplitSeq(m[1], ", ") {
			action, err := strconv.Unquote(a)
			if err != nil {
				t.Fatalf("legacy action %s: %v", a, err)
			}
			actions = append(actions, action)
		}
		usedBy, err := strconv.Unquote(m[2])
		if err != nil {
			t.Fatalf("legacy use %s: %v", m[2], err)
		}
		legacy = append(legacy, Permission{Actions: actions, UsedBy: usedBy})
	}
	if len(legacy) < 20 {
		t.Fatalf("read %d rows from %s; the pattern no longer matches the file", len(legacy), path)
	}
	if diff := cmp.Diff(legacy, RequiredPermissions()); diff != "" {
		t.Errorf("RequiredPermissions() differs from the legacy table (-legacy +new):\n%s", diff)
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
