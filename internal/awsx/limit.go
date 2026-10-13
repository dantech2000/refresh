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
// steps.
type callState struct {
	// held is true while the call holds the slot that it got before its
	// first attempt.
	held bool
}

type callStateKey struct{}

// add puts the two steps of the limiter on the stack of a call.
//
// The first step runs once, before anything else of the call. It waits for
// the slot of the first attempt. A sweep has many more calls waiting than
// in flight, and each waits here, where the stack of its goroutine is still
// small. A wait at the attempt itself would park every goroutine below the
// whole SDK stack, and the stacks of some thousand waiting calls were half
// the memory of a sweep.
//
// The second step runs once for each attempt. It sits after the retry step
// and before signing, so a slot covers the credentials, the signature, the
// round trip, and the read of the answer. The slot is given back after
// each attempt: it is free during a backoff and between the pages of a
// paged call. A retry waits for a new slot at this step.
func (l *limiter) add(stack *middleware.Stack) error {
	err := stack.Initialize.Add(middleware.InitializeMiddlewareFunc("refresh:RequestLimitFirst", l.first), middleware.Before)
	if err != nil {
		return err
	}
	return stack.Finalize.Insert(middleware.FinalizeMiddlewareFunc("refresh:RequestLimit", l.attempt), "Retry", middleware.After)
}

// first waits for the slot of the first attempt and runs the call. A call
// that gets no slot before the context ends was never sent.
func (l *limiter) first(ctx context.Context, in middleware.InitializeInput, next middleware.InitializeHandler) (middleware.InitializeOutput, middleware.Metadata, error) {
	select {
	case l.slots <- struct{}{}:
	case <-ctx.Done():
		return middleware.InitializeOutput{}, middleware.Metadata{}, fmt.Errorf("%w: %w", ErrNotSent, ctx.Err())
	}
	state := &callState{held: true}
	// A call that fails before its first attempt, such as one whose input
	// does not serialize, still gives the slot back.
	defer func() {
		if state.held {
			<-l.slots
		}
	}()
	return next.HandleInitialize(context.WithValue(ctx, callStateKey{}, state), in)
}

// attempt runs one attempt of a call inside a slot: the one that the call
// already holds for its first attempt, or a new one for a retry.
func (l *limiter) attempt(ctx context.Context, in middleware.FinalizeInput, next middleware.FinalizeHandler) (middleware.FinalizeOutput, middleware.Metadata, error) {
	// The attempts of one call run one after another, so the state needs
	// no lock.
	state, _ := ctx.Value(callStateKey{}).(*callState)
	if state != nil && state.held {
		state.held = false
	} else {
		select {
		case l.slots <- struct{}{}:
		case <-ctx.Done():
			// The first attempt did reach AWS, so this is not a call that
			// was never sent: it ends with the error of the context.
			return middleware.FinalizeOutput{}, middleware.Metadata{}, ctx.Err()
		}
	}
	defer func() { <-l.slots }()
	return next.HandleFinalize(ctx, in)
}
