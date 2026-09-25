package tui

import (
	"testing"

	"go.uber.org/goleak"

	"github.com/dantech2000/refresh/internal/render"
)

func TestMain(m *testing.M) {
	// The frame assertions use the Unicode glyphs, whatever the locale of
	// the machine running the tests.
	glyphs = render.New(render.ColorNone, true)
	goleak.VerifyTestMain(m)
}
