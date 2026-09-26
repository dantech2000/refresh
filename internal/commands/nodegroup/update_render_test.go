package nodegroup

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/dantech2000/refresh/internal/render"
)

// A pod check that did not run renders as not measured (the neutral glyph),
// never with the passed glyph; checks that ran keep the passed glyph.
func TestVerificationLines_SkippedCheckIsNotPassed(t *testing.T) {
	v, _ := verifyPostRoll(context.Background(), activeNodegroupEKS(), nil, "c", []string{"ng-a"}, nil, false)
	lines := verificationLines(render.New(render.ColorNone, true), v)
	joined := strings.Join(lines, "\n")
	for _, want := range []string{
		"● Post-roll verification passed:",
		"  ● nodegroup ng-a is ACTIVE",
		"  • pod verification skipped (no Kubernetes access)",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("verification block missing %q:\n%s", want, joined)
		}
	}

	ascii := strings.Join(verificationLines(render.New(render.ColorNone, false), v), "\n")
	for _, want := range []string{"[OK] Post-roll verification passed:", "  - pod verification skipped"} {
		if !strings.Contains(ascii, want) {
			t.Errorf("ASCII block missing %q:\n%s", want, ascii)
		}
	}
	if strings.ContainsAny(ascii, "●•✗\x1b") {
		t.Errorf("ASCII block has Unicode glyphs or ANSI:\n%s", ascii)
	}
}

func TestVerificationLines_Issues(t *testing.T) {
	v := PostRollVerification{Checks: []string{"nodegroup ng-a is ACTIVE"}, Issues: []string{"2 pod(s) newly Pending after roll"}}
	joined := strings.Join(verificationLines(render.New(render.ColorNone, true), v), "\n")
	for _, want := range []string{"✗ Post-roll verification found issues:", "  ✗ 2 pod(s) newly Pending after roll", "  ● nodegroup ng-a is ACTIVE"} {
		if !strings.Contains(joined, want) {
			t.Errorf("verification block missing %q:\n%s", want, joined)
		}
	}
}

// The skipped marker is view-only: the document keeps the check text in
// checks, as before.
func TestPostRollVerification_SkippedNotSerialized(t *testing.T) {
	var v PostRollVerification
	v.Skip("pod verification skipped (no Kubernetes access)")
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(b), `{"checks":["pod verification skipped (no Kubernetes access)"]}`; got != want {
		t.Errorf("json = %s, want %s", got, want)
	}
}
