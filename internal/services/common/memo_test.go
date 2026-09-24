package common

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
