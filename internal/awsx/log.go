package awsx

import (
	"context"
	"log/slog"
	"time"

	awsmiddleware "github.com/aws/aws-sdk-go-v2/aws/middleware"
	"github.com/aws/smithy-go/middleware"
)

// logAttemptsID names the step that logs each attempt of an AWS call.
const logAttemptsID = "refresh:LogAttempts"

// logAttempts adds a step to each call that logs every attempt at the debug
// level: the service, the operation, the region, how long it took, and the
// class of its error. It runs after the retry step, so a retried call logs
// one line for each attempt. Nothing is measured when the default logger
// does not log debug lines.
func logAttempts(stack *middleware.Stack) error {
	mw := middleware.FinalizeMiddlewareFunc(logAttemptsID,
		func(ctx context.Context, in middleware.FinalizeInput, next middleware.FinalizeHandler) (middleware.FinalizeOutput, middleware.Metadata, error) {
			logger := slog.Default()
			if !logger.Enabled(ctx, slog.LevelDebug) {
				return next.HandleFinalize(ctx, in)
			}
			start := time.Now()
			out, metadata, err := next.HandleFinalize(ctx, in)
			attrs := []any{
				"service", awsmiddleware.GetServiceID(ctx),
				"operation", awsmiddleware.GetOperationName(ctx),
				"region", awsmiddleware.GetRegion(ctx),
				"duration", time.Since(start).Round(time.Millisecond),
			}
			if err != nil {
				attrs = append(attrs, "class", string(Classify(err)), "retryable", IsRetryable(err), "error", err)
				logger.DebugContext(ctx, "AWS call attempt failed", attrs...)
			} else {
				logger.DebugContext(ctx, "AWS call", attrs...)
			}
			return out, metadata, err
		})
	// A client without a retry step (none today) still logs its calls.
	if err := stack.Finalize.Insert(mw, "Retry", middleware.After); err != nil {
		return stack.Finalize.Add(mw, middleware.After)
	}
	return nil
}
