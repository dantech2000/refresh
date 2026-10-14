package awsx

import (
	"errors"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

// TestRequiredPermissionsMatchLegacy reads the IAM table of the old tree as
// text, because Go's internal rule keeps this package from importing it, and
// checks that this table has the same actions in the same rows and order.
// The text of a row is not compared: it names the commands and flags of the
// v2 contract, which differ from the old ones. The rows in added are the
// actions that only the new tree calls.
//
// Remove this test at cutover. From then on the docs table is checked
// against this table alone.
func TestRequiredPermissionsMatchLegacy(t *testing.T) {
	const path = "../../legacy/internal/aws/awserr/permissions.go"
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the legacy table: %v", err)
	}
	row := regexp.MustCompile(`(?m)^\t\{\[\]string\{(.*)\}, "(?:[^"\\]|\\.)*"\},$`)
	var legacy [][]string
	for _, m := range row.FindAllStringSubmatch(string(src), -1) {
		var actions []string
		for a := range strings.SplitSeq(m[1], ", ") {
			action, err := strconv.Unquote(a)
			if err != nil {
				t.Fatalf("legacy action %s: %v", a, err)
			}
			actions = append(actions, action)
		}
		legacy = append(legacy, actions)
	}
	if len(legacy) < 20 {
		t.Fatalf("read %d rows from %s; the pattern no longer matches the file", len(legacy), path)
	}
	// added are the rows of the actions that the new tree calls and the old
	// one does not: the vCPUs of an instance type, for the quota check of a
	// roll and a scale-up.
	added := [][]string{{"ec2:DescribeInstanceTypes"}}
	var got [][]string
	for _, p := range RequiredPermissions() {
		if !slices.ContainsFunc(added, func(a []string) bool { return slices.Equal(a, p.Actions) }) {
			got = append(got, p.Actions)
		}
	}
	if diff := cmp.Diff(legacy, got); diff != "" {
		t.Errorf("the actions of RequiredPermissions() differ from the legacy table (-legacy +new):\n%s", diff)
	}
}

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
