package aws

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks/types"

	"github.com/dantech2000/refresh/internal/mocks"
)

func TestNodegroupK8sVersion(t *testing.T) {
	if got := NodegroupK8sVersion(&types.Nodegroup{Version: aws.String("1.31")}, "1.32"); got != "1.31" {
		t.Errorf("nodegroup version: got %q, want 1.31", got)
	}
	if got := NodegroupK8sVersion(&types.Nodegroup{}, "1.32"); got != "1.32" {
		t.Errorf("empty nodegroup version: got %q, want cluster fallback 1.32", got)
	}
	if got := NodegroupK8sVersion(nil, "1.32"); got != "1.32" {
		t.Errorf("nil nodegroup: got %q, want cluster fallback 1.32", got)
	}
}

func TestLatestAMICache_KeysByVersionAndTypeAndDedupesConcurrentLookups(t *testing.T) {
	var calls atomic.Int32
	cache := NewLatestAMICache(func(_ context.Context, v string, at types.AMITypes) (string, error) {
		calls.Add(1)
		return "ami-" + v + "-" + string(at), nil
	})

	ctx := context.Background()
	const want131 = "ami-1.31-AL2023_x86_64_STANDARD"
	var wg sync.WaitGroup
	for range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if got, err := cache.Get(ctx, "1.31", types.AMITypesAl2023X8664Standard); err != nil || got != want131 {
				t.Errorf("Get = %q, %v; want %q, nil", got, err, want131)
			}
		}()
	}
	wg.Wait()
	if n := calls.Load(); n != 1 {
		t.Fatalf("lookups for one key = %d, want 1", n)
	}

	ng131 := &types.Nodegroup{Version: aws.String("1.31"), AmiType: types.AMITypesAl2023X8664Standard}
	ng132 := &types.Nodegroup{Version: aws.String("1.32"), AmiType: types.AMITypesAl2023X8664Standard}
	if got, _ := cache.ForNodegroup(ctx, ng131, "1.32"); got != want131 {
		t.Errorf("ForNodegroup(1.31) = %q, want %q", got, want131)
	}
	if got, _ := cache.ForNodegroup(ctx, ng132, "1.32"); got != "ami-1.32-AL2023_x86_64_STANDARD" {
		t.Errorf("ForNodegroup(1.32) = %q", got)
	}
	if n := calls.Load(); n != 2 {
		t.Fatalf("total lookups = %d, want 2 (one per distinct version)", n)
	}
}

// A failed lookup is returned to the caller but not memoized: the next Get
// looks it up again, and a later success is then cached.
func TestLatestAMICache_DoesNotCacheFailures(t *testing.T) {
	errThrottled := mocks.Throttling()
	var calls atomic.Int32
	cache := NewLatestAMICache(func(context.Context, string, types.AMITypes) (string, error) {
		if calls.Add(1) == 1 {
			return "", errThrottled
		}
		return "ami-latest", nil
	})
	ctx := context.Background()

	if got, err := cache.Get(ctx, "1.31", types.AMITypesAl2X8664); !errors.Is(err, errThrottled) || got != "" {
		t.Fatalf("first Get = %q, %v; want \"\", the lookup error", got, err)
	}
	for range 3 {
		if got, err := cache.Get(ctx, "1.31", types.AMITypesAl2X8664); err != nil || got != "ami-latest" {
			t.Fatalf("Get after failure = %q, %v; want ami-latest, nil", got, err)
		}
	}
	if n := calls.Load(); n != 2 {
		t.Fatalf("lookups = %d, want 2 (failure retried once, success cached)", n)
	}
}

// Waiters on an in-flight lookup share its result, including its error.
func TestLatestAMICache_ConcurrentWaitersShareFailure(t *testing.T) {
	errDenied := mocks.AccessDenied()
	release := make(chan struct{})
	var calls atomic.Int32
	cache := NewLatestAMICache(func(context.Context, string, types.AMITypes) (string, error) {
		calls.Add(1)
		<-release
		return "", errDenied
	})

	const n = 20
	errs := make(chan error, n)
	for range n {
		go func() {
			_, err := cache.Get(context.Background(), "1.31", types.AMITypesAl2X8664)
			errs <- err
		}()
	}
	// Let every goroutine join the single flight before it completes.
	deadline := time.Now().Add(2 * time.Second)
	for calls.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	time.Sleep(20 * time.Millisecond)
	close(release)
	for range n {
		if err := <-errs; !errors.Is(err, errDenied) {
			t.Errorf("Get err = %v, want the shared lookup error", err)
		}
	}
	if got := calls.Load(); got < 1 || got > n {
		t.Errorf("lookups = %d, want between 1 and %d", got, n)
	}
}

// When the goroutine that owns an in-flight lookup is cancelled, a waiter
// whose own ctx is live must not inherit that cancellation: it looks up again.
func TestLatestAMICache_OwnerCancellationDoesNotPoisonWaiters(t *testing.T) {
	ownerCtx, cancelOwner := context.WithCancel(context.Background())
	started := make(chan struct{})
	var calls atomic.Int32
	cache := NewLatestAMICache(func(ctx context.Context, _ string, _ types.AMITypes) (string, error) {
		if calls.Add(1) == 1 {
			close(started)
			<-ctx.Done()
			return "", ctx.Err()
		}
		return "ami-latest", nil
	})

	ownerErr := make(chan error, 1)
	go func() {
		_, err := cache.Get(ownerCtx, "1.31", types.AMITypesAl2X8664)
		ownerErr <- err
	}()
	<-started

	waiter := make(chan string, 1)
	go func() {
		got, err := cache.Get(context.Background(), "1.31", types.AMITypesAl2X8664)
		if err != nil {
			t.Errorf("waiter Get err = %v, want nil", err)
		}
		waiter <- got
	}()
	time.Sleep(20 * time.Millisecond) // let the waiter block on the flight
	cancelOwner()

	if err := <-ownerErr; !errors.Is(err, context.Canceled) {
		t.Errorf("owner Get err = %v, want context.Canceled", err)
	}
	if got := <-waiter; got != "ami-latest" {
		t.Errorf("waiter Get = %q, want ami-latest", got)
	}
}

// A waiter whose own ctx ends stops waiting and reports its own ctx error.
func TestLatestAMICache_WaiterHonorsOwnContext(t *testing.T) {
	release := make(chan struct{})
	started := make(chan struct{})
	cache := NewLatestAMICache(func(context.Context, string, types.AMITypes) (string, error) {
		close(started)
		<-release
		return "ami-latest", nil
	})
	defer close(release)

	go func() { _, _ = cache.Get(context.Background(), "1.31", types.AMITypesAl2X8664) }()
	<-started

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := cache.Get(ctx, "1.31", types.AMITypesAl2X8664); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Get err = %v, want context.DeadlineExceeded", err)
	}
}
