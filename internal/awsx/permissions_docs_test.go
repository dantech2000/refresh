package awsx

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

// permissionsDoc returns the docs page that holds the IAM table.
func permissionsDoc(t *testing.T) string {
	t.Helper()
	path := filepath.Join("..", "..", "docs", "concepts", "configuration.md")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("looking for the IAM table: %v", err)
	}
	return path
}

// TestRequiredPermissionsMatchDocs checks that the table under "Required IAM
// permissions" in the docs is RequiredPermissions, row for row: the same
// actions in the same order with the same text. The permission-error hint
// links to that table.
func TestRequiredPermissionsMatchDocs(t *testing.T) {
	path := permissionsDoc(t)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the docs: %v", err)
	}
	const heading = "## Required IAM permissions\n"
	_, section, ok := strings.Cut(string(data), heading)
	if !ok {
		t.Fatalf("%s has no %q section; PermissionsDocURL links to it", path, strings.TrimSpace(heading))
	}

	var docs []Permission
	inTable := false
	for line := range strings.SplitSeq(section, "\n") {
		if !strings.HasPrefix(line, "|") {
			if inTable {
				break
			}
			continue
		}
		inTable = true
		cells := strings.Split(strings.Trim(line, "|"), "|")
		if len(cells) != 2 {
			t.Fatalf("table row %q: want 2 cells, got %d", line, len(cells))
		}
		actionCell, usedBy := strings.TrimSpace(cells[0]), strings.TrimSpace(cells[1])
		if actionCell == "Action" || strings.HasPrefix(actionCell, "---") {
			continue
		}
		var actions []string
		for a := range strings.SplitSeq(actionCell, ",") {
			actions = append(actions, strings.Trim(strings.TrimSpace(a), "`"))
		}
		docs = append(docs, Permission{Actions: actions, UsedBy: usedBy})
	}
	if len(docs) == 0 {
		t.Fatalf("%s: no table under %q", path, strings.TrimSpace(heading))
	}
	if diff := cmp.Diff(RequiredPermissions(), docs); diff != "" {
		t.Errorf("the IAM table of %s differs from RequiredPermissions() (-code +docs):\n%s", path, diff)
	}
}
