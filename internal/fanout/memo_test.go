package fanout

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
)

func TestMemo_DedupesConcurrentLookupsPerKey(t *testing.T) {
	var m Memo[string, int]
	var calls atomic.Int32
	release := make(chan struct{})
	lookup := func(context.Context) (int, error) {
		calls.Add(1)
		<-release
		return 7, nil
	}

	var wg sync.WaitGroup
	results := make([]int, 8)
	for i := range results {
		wg.Go(func() {
			v, err := m.Get(context.Background(), "k", lookup)
			if err != nil {
				t.Errorf("Get: %v", err)
			}
			results[i] = v
		})
	}
	close(release)
	wg.Wait()

	if n := calls.Load(); n != 1 {
		t.Errorf("lookups = %d, want 1", n)
	}
	for i, v := range results {
		if v != 7 {
			t.Errorf("result[%d] = %d, want 7", i, v)
		}
	}
	if _, err := m.Get(context.Background(), "other", func(context.Context) (int, error) { return 1, nil }); err != nil {
		t.Fatalf("Get other key: %v", err)
	}
}

func TestMemo_DoesNotCacheFailures(t *testing.T) {
	var m Memo[string, int]
	var calls atomic.Int32
	boom := errors.New("boom")
	lookup := func(context.Context) (int, error) {
		if calls.Add(1) == 1 {
			return 0, boom
		}
		return 3, nil
	}

	if _, err := m.Get(context.Background(), "k", lookup); !errors.Is(err, boom) {
		t.Fatalf("first Get err = %v, want boom", err)
	}
	v, err := m.Get(context.Background(), "k", lookup)
	if err != nil || v != 3 {
		t.Fatalf("second Get = %d, %v; want 3, nil", v, err)
	}
	if v, _ := m.Get(context.Background(), "k", lookup); v != 3 || calls.Load() != 2 {
		t.Errorf("third Get = %d after %d lookups; want 3 from cache after 2", v, calls.Load())
	}
}

func TestMemo_WaiterCancellationReturnsCtxErr(t *testing.T) {
	var m Memo[string, int]
	started := make(chan struct{})
	release := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = m.Get(context.Background(), "k", func(context.Context) (int, error) {
			close(started)
			<-release
			return 1, nil
		})
	}()
	<-started

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := m.Get(ctx, "k", func(context.Context) (int, error) { return 2, nil }); !errors.Is(err, context.Canceled) {
		t.Errorf("waiter err = %v, want context.Canceled", err)
	}
	close(release)
	<-done
}

// A lookup that panics must not leave its zero value cached as a success.
func TestMemo_PanicIsNotCached(t *testing.T) {
	var m Memo[string, int]
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("expected the lookup's panic to propagate")
			}
		}()
		_, _ = m.Get(context.Background(), "k", func(context.Context) (int, error) { panic("boom") })
	}()

	v, err := m.Get(context.Background(), "k", func(context.Context) (int, error) { return 5, nil })
	if err != nil || v != 5 {
		t.Fatalf("Get after panic = %d, %v; want 5, nil from a fresh lookup", v, err)
	}
}

// A lookup that timed out inside (a dial timeout is a DeadlineExceeded)
// while its caller's ctx was live is a plain failure: a waiter gets the
// error and does not run the lookup again. With N waiters, a retry by each
// would repeat the timeout N times, one after the other.
func TestMemo_WaiterDoesNotRetryATimeoutInsideTheLookup(t *testing.T) {
	// The entry of a lookup that has just failed, as a waiter sees it
	// before the owner removes it.
	failed := &memoEntry[int]{done: make(chan struct{}), err: context.DeadlineExceeded}
	close(failed.done)
	m := Memo[string, int]{entries: map[string]*memoEntry[int]{"k": failed}}

	_, err := m.Get(context.Background(), "k", func(context.Context) (int, error) {
		t.Error("the waiter ran the lookup again")
		return 0, nil
	})

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("waiter err = %v, want the timeout of the lookup", err)
	}
}

// run records whether the ctx of the caller that ran the lookup had ended.
func TestMemo_RecordsWhetherTheOwnerGaveUp(t *testing.T) {
	tests := []struct {
		name      string
		cancelled bool
		err       error
		want      bool
	}{
		{name: "owner cancelled", cancelled: true, err: context.Canceled, want: true},
		{name: "timeout inside the lookup", err: context.DeadlineExceeded},
		{name: "other failure", err: errors.New("boom")},
		{name: "success"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tt.cancelled {
				cancel()
			}
			var m Memo[string, int]
			e := &memoEntry[int]{done: make(chan struct{})}
			_, _ = m.run(ctx, "k", e, func(context.Context) (int, error) { return 0, tt.err })
			if e.ownerDone != tt.want {
				t.Errorf("ownerDone = %t, want %t", e.ownerDone, tt.want)
			}
		})
	}
}

// When the caller that ran the lookup gave up, a waiter whose own ctx is
// live looks the key up again and does not inherit the cancellation.
func TestMemo_WaiterRetriesAfterTheOwnerGaveUp(t *testing.T) {
	var m Memo[string, int]
	started := make(chan struct{})
	ownerCtx, cancelOwner := context.WithCancel(context.Background())
	ownerDone := make(chan struct{})
	go func() {
		defer close(ownerDone)
		_, _ = m.Get(ownerCtx, "k", func(ctx context.Context) (int, error) {
			close(started)
			<-ctx.Done()
			return 0, ctx.Err()
		})
	}()
	<-started

	waiter := make(chan int, 1)
	go func() {
		v, err := m.Get(context.Background(), "k", func(context.Context) (int, error) { return 2, nil })
		if err != nil {
			v = -1
		}
		waiter <- v
	}()
	cancelOwner()
	<-ownerDone

	// The waiter gets 2 whether it waited on the owner or came after it.
	if got := <-waiter; got != 2 {
		t.Errorf("waiter got %d, want 2 from its own lookup", got)
	}
}
