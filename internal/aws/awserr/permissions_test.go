package awserr

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

const repoRoot = "../../.."

// docsPermissionRows parses the table under "## Required IAM permissions" in
// docs/concepts/configuration.md.
func docsPermissionRows(t *testing.T) []Permission {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(repoRoot, "docs", "concepts", "configuration.md"))
	if err != nil {
		t.Fatalf("reading configuration.md: %v", err)
	}
	_, section, ok := strings.Cut(string(data), "## Required IAM permissions\n")
	if !ok {
		t.Fatal("configuration.md has no \"## Required IAM permissions\" section")
	}
	var rows []Permission
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
		rows = append(rows, Permission{Actions: actions, UsedBy: usedBy})
	}
	return rows
}

func TestRequiredPermissions_MatchDocsTable(t *testing.T) {
	docs := docsPermissionRows(t)
	if len(docs) != len(RequiredPermissions) {
		t.Errorf("docs table has %d rows, RequiredPermissions has %d", len(docs), len(RequiredPermissions))
	}
	for i := range min(len(docs), len(RequiredPermissions)) {
		want, got := RequiredPermissions[i], docs[i]
		if !slices.Equal(want.Actions, got.Actions) || want.UsedBy != got.UsedBy {
			t.Errorf("row %d differs:\n  code: %v | %s\n  docs: %v | %s", i, want.Actions, want.UsedBy, got.Actions, got.UsedBy)
		}
	}
}

// awsCallRE matches an SDK input literal such as eks.DescribeClusterInput{.
var awsCallRE = regexp.MustCompile(`\b(eks|ec2|ssm|sts|cloudwatch|autoscaling|servicequotas)\.([A-Z]\w*)Input\{`)

// TestRequiredPermissions_CoverCode checks that every AWS API the code calls
// is listed, and that every listed action is still called.
func TestRequiredPermissions_CoverCode(t *testing.T) {
	called := map[string]bool{}
	for _, dir := range []string{"internal", "main.go"} {
		err := filepath.WalkDir(filepath.Join(repoRoot, dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if d.Name() == "mocks" {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			src, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			for _, m := range awsCallRE.FindAllStringSubmatch(string(src), -1) {
				called[m[1]+":"+m[2]] = true
			}
			return nil
		})
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("walking %s: %v", dir, err)
		}
	}

	listed := map[string]bool{}
	for _, p := range RequiredPermissions {
		for _, a := range p.Actions {
			listed[a] = true
		}
	}
	for a := range called {
		if !listed[a] {
			t.Errorf("code calls %s, but RequiredPermissions does not list it", a)
		}
	}
	for a := range listed {
		if !called[a] {
			t.Errorf("RequiredPermissions lists %s, but no code calls it", a)
		}
	}
}

func TestFormatPermissionError_ListsEveryRequiredAction(t *testing.T) {
	msg := formatPermissionError(errors.New("denied"), "listing clusters").Error()
	for _, p := range RequiredPermissions {
		for _, a := range p.Actions {
			if !strings.Contains(msg, a) {
				t.Errorf("permission hint is missing %s", a)
			}
		}
	}
	if strings.Contains(msg, "GetMetricStatistics") {
		t.Error("permission hint still names cloudwatch:GetMetricStatistics")
	}
	if !strings.Contains(msg, PermissionsDocURL) {
		t.Error("permission hint does not link the docs table")
	}
	if strings.Contains(msg, "`") {
		t.Error("permission hint contains Markdown backticks")
	}
}
