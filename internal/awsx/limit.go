package awsx

import (
	"context"
	"errors"
	"fmt"

	"github.com/aws/smithy-go/middleware"
)

// ErrNotSent is in the chain of the error of a call whose first attempt
// never left: the context ended while the call waited for a slot of the
// region's request limit. Nothing reached AWS.
var ErrNotSent = errors.New("request not sent")

// limiter bounds the requests in flight for one region. The provider keeps
// one for each region, and every client set of the region shares it.
type limiter struct {
	slots chan struct{}
}

func newLimiter(n int) *limiter {
	return &limiter{slots: make(chan struct{}, n)}
}

// callState is what the limiter remembers about one call between its
// attempts.
type callState struct {
	sent bool // an attempt got a slot
}

type callStateKey struct{}

// add puts the two steps of the limiter on the stack of a call. The first
// step runs once for the call and gives it a state. The second step runs
// once for each attempt: it sits after the retry step and before signing,
// so a slot covers the credentials, the signature, the round trip, and the
// read of the answer, and is free during a backoff and between the pages of
// a paged call.
func (l *limiter) add(stack *middleware.Stack) error {
	err := stack.Initialize.Add(middleware.InitializeMiddlewareFunc("refresh:RequestLimitState",
		func(ctx context.Context, in middleware.InitializeInput, next middleware.InitializeHandler) (middleware.InitializeOutput, middleware.Metadata, error) {
			return next.HandleInitialize(context.WithValue(ctx, callStateKey{}, &callState{}), in)
		}), middleware.Before)
	if err != nil {
		return err
	}
	return stack.Finalize.Insert(middleware.FinalizeMiddlewareFunc("refresh:RequestLimit", l.attempt), "Retry", middleware.After)
}

// attempt runs one attempt of a call inside a slot.
func (l *limiter) attempt(ctx context.Context, in middleware.FinalizeInput, next middleware.FinalizeHandler) (middleware.FinalizeOutput, middleware.Metadata, error) {
	// The attempts of one call run one after another, so the state needs
	// no lock.
	state, _ := ctx.Value(callStateKey{}).(*callState)
	if state == nil {
		state = &callState{}
	}
	select {
	case l.slots <- struct{}{}:
	case <-ctx.Done():
		// Only a call whose first attempt got no slot was never sent. A
		// retry that gets none ends with the error of the context.
		err := ctx.Err()
		if !state.sent {
			err = fmt.Errorf("%w: %w", ErrNotSent, err)
		}
		return middleware.FinalizeOutput{}, middleware.Metadata{}, err
	}
	defer func() { <-l.slots }()
	state.sent = true
	return next.HandleFinalize(ctx, in)
}
