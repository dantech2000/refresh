package nodegroup

import (
	"context"
	"time"

	"github.com/dantech2000/refresh/internal/services/common"
)

// monitorFunc runs the EKS update monitor, printing progress unless quiet,
// until the updates are terminal, ctx ends, or the monitor's own timeout
// elapses.
type monitorFunc func(ctx context.Context, quiet bool) error

// monitorAlongsidePanel runs the EKS monitor next to the live roll panel. The
// monitor is quiet only while the panel is drawing. If the panel returns while
// the update is still running (nothing to observe, kube context mismatch,
// baseline failure, or the roll already looks complete), the quiet monitor is
// stopped and restarted with normal progress output. The restarted run keeps
// the original deadline (start + timeout), so the total wait never grows.
//
// heldBack reports whether the returned result came from the quiet run, in
// which case the caller must print the completion summary or stop banner the
// monitor held back.
func monitorAlongsidePanel(ctx context.Context, panel func(context.Context), timeout time.Duration, monitor monitorFunc) (heldBack bool, err error) {
	deadline := time.Now().Add(timeout)
	quietCtx, stopQuiet := context.WithCancel(ctx)
	defer stopQuiet()
	var stoppedByPanel bool

	err = common.RunAlongside(ctx, func(pctx context.Context) {
		defer stopQuiet() // the panel is gone: let the monitor speak
		panel(pctx)
	}, func(context.Context) error {
		merr := monitor(quietCtx, true)
		// Only the panel's early return cancels quietCtx while ctx is live;
		// once this func returns, RunAlongside cancels the panel instead.
		stoppedByPanel = quietCtx.Err() != nil && ctx.Err() == nil
		return merr
	})

	// The quiet run ended on its own (terminal, timeout, or user cancel), or
	// the parent context is done: its result stands.
	if !stoppedByPanel {
		return true, err
	}
	loudCtx := ctx
	if timeout > 0 {
		var cancel context.CancelFunc
		loudCtx, cancel = context.WithDeadline(ctx, deadline)
		defer cancel()
	}
	return false, monitor(loudCtx, false)
}
