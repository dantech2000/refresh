package commands

import (
	"strings"
	"testing"

	"github.com/dantech2000/refresh/internal/apidoc"
)

// The schema page lists every kind, with its command and a link to its
// schema file.
func TestRenderSchemaIndex(t *testing.T) {
	page := renderSchemaIndex("../schema/v1")
	if !strings.HasPrefix(page, generatedNote) {
		t.Error("the page has no generated-file note")
	}
	for _, k := range apidoc.Kinds() {
		row := "| `" + string(k) + "` | `refresh " + k.Command() + "` | [" + string(k) + ".json](../schema/v1/" + string(k) + ".json) |"
		if !strings.Contains(page, row) {
			t.Errorf("page missing the row\n%s\n---\n%s", row, page)
		}
		if k.Command() == "" {
			t.Errorf("kind %s has no command", k)
		}
	}
}
