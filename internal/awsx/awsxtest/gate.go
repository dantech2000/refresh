package awsxtest

import (
	"context"
	"testing"

	"github.com/dantech2000/refresh/internal/fleet/fleettest"
)

// NewGate puts a fleettest.Gate on h. It takes the OnRequest hook of the
// handler, so the test must set no hook of its own, and it must do so
// before the first request.
func NewGate(tb testing.TB, h *Handler) *fleettest.Gate {
	tb.Helper()
	g := fleettest.NewGate(tb)
	h.OnRequest = func(ctx context.Context, r Request) { g.Hold(ctx, r.Operation) }
	return g
}
