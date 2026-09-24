package common

import (
	"context"
	"sync"
)

// RunAlongside runs observe concurrently with wait and returns wait's result.
// wait is authoritative (e.g. the EKS DescribeUpdate poll); observe is a
// best-effort side view (e.g. the live node-roll panel) that must never block
// or decide the result.
//
// When wait returns, for any reason (success, failure, cancellation), observe's
// context is cancelled and RunAlongside blocks until observe has returned, so
// no goroutine outlives the call and nothing observe writes can land after it.
// observe may also return early on its own (e.g. the roll looks complete);
// wait keeps running regardless. A nil observe just runs wait.
func RunAlongside(ctx context.Context, observe func(context.Context), wait func(context.Context) error) error {
	if observe == nil {
		return wait(ctx)
	}
	obsCtx, cancel := context.WithCancel(ctx)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		observe(obsCtx)
	}()
	err := wait(ctx)
	cancel()
	wg.Wait()
	return err
}
