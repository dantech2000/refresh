package awsx

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/eks"

	"github.com/dantech2000/refresh/internal/awsx/awsxtest"
	"github.com/dantech2000/refresh/internal/fleet/fleettest"
)

// A context that has ended gets no slot, although one is free.
func TestLimiterAcquireWithEndedContext(t *testing.T) {
	l := newLimiter(1)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	// With a free slot and an ended context, select may pick either case:
	// many runs make a wrong pick show up.
	for range 1000 {
		if err := l.acquire(ctx); !errors.Is(err, context.Canceled) {
			t.Fatalf("acquire() error = %v, want context.Canceled", err)
		}
		if held := len(l.slots); held != 0 {
			t.Fatalf("slots held = %d, want 0", held)
		}
	}
}

// endsDuringWait is a context that ends between the check before the wait
// and the wait itself: its first Err is nil, and Done is closed.
type endsDuringWait struct {
	context.Context
	errCalls atomic.Int32
}

func (c *endsDuringWait) Err() error {
	if c.errCalls.Add(1) == 1 {
		return nil
	}
	return context.Canceled
}

// The context ends at the moment that a slot is free. Whichever case select
// picks, the call gets no slot and none stays taken.
func TestLimiterAcquireWhenContextEndsWithAFreeSlot(t *testing.T) {
	ended, cancel := context.WithCancel(context.Background())
	cancel()
	for range 1000 {
		l := newLimiter(1)
		if err := l.acquire(&endsDuringWait{Context: ended}); !errors.Is(err, context.Canceled) {
			t.Fatalf("acquire() error = %v, want context.Canceled", err)
		}
		if held := len(l.slots); held != 0 {
			t.Fatalf("slots held = %d, want 0: the slot taken in the race was not given back", held)
		}
	}
}

func TestLimiterAcquire(t *testing.T) {
	l := newLimiter(1)
	if err := l.acquire(context.Background()); err != nil {
		t.Fatalf("acquire() error = %v", err)
	}
	if held := len(l.slots); held != 1 {
		t.Errorf("slots held = %d, want 1", held)
	}
}

// A call under an ended context, with every slot free, was never sent: its
// error says so, and nothing reaches the server.
func TestCallWithEndedContextIsNotSent(t *testing.T) {
	srv := awsxtest.NewServer(fleettest.New())
	defer srv.Close()
	provider := NewProvider(srv.Config("us-east-1"), func(o *ProviderOptions) {
		o.Backoff = zeroBackoff{}
		o.MaxInFlight = 4
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	for range 200 {
		_, err := provider.Clients("").EKS.ListClusters(ctx, &eks.ListClustersInput{})
		if !errors.Is(err, ErrNotSent) || !errors.Is(err, context.Canceled) {
			t.Fatalf("ListClusters error = %v, want ErrNotSent with context.Canceled", err)
		}
	}

	if n := len(srv.Requests()); n != 0 {
		t.Errorf("requests = %d, want 0", n)
	}
	if held := len(provider.limiters["us-east-1"].slots); held != 0 {
		t.Errorf("slots held = %d, want 0", held)
	}
}
