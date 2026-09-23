package statuscmd

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/urfave/cli/v3"

	"github.com/dantech2000/refresh/internal/commands/runner"
	statussvc "github.com/dantech2000/refresh/internal/services/status"
)

// ctxRegion answers for us-east-1, then closes answered. Any other region
// waits for that handshake and then runs block, so the sweep always has one
// answered region before the interrupt or the deadline, whatever order the
// regions are dispatched in.
type ctxRegion struct {
	region   string
	answered chan struct{}
	once     *sync.Once
	block    func(ctx context.Context) error
}

func (f ctxRegion) ListClusterStatuses(ctx context.Context, _ statussvc.ListOptions) ([]statussvc.ClusterStatus, error) {
	if f.region == "us-east-1" {
		defer f.once.Do(func() { close(f.answered) })
		return []statussvc.ClusterStatus{{
			Name: "prod", Region: "us-east-1", Version: "1.33",
			Support: statussvc.SupportPosture{Tier: statussvc.SupportStandard},
		}}, nil
	}
	<-f.answered
	return nil, f.block(ctx)
}

// stubTwoRegions stubs the region service with ctxRegion sharing one
// handshake.
func stubTwoRegions(t *testing.T, block func(ctx context.Context) error) {
	t.Helper()
	answered, once := make(chan struct{}), &sync.Once{}
	stubRegionService(t, func(cfg aws.Config) regionLister {
		return ctxRegion{region: cfg.Region, answered: answered, once: once, block: block}
	})
}

func runStatusCtx(ctx context.Context, t *testing.T, args ...string) error {
	t.Helper()
	root := &cli.Command{
		Name: "refresh",
		Flags: []cli.Flag{
			&cli.DurationFlag{Name: "timeout", Value: time.Minute},
			&cli.IntFlag{Name: "max-concurrency", Value: 4},
			&cli.StringFlag{Name: "region"},
		},
		Commands:       []*cli.Command{Command()},
		ExitErrHandler: func(context.Context, *cli.Command, error) {},
	}
	return root.Run(ctx, append([]string{"refresh", "status"}, args...))
}

// Ctrl+C after one region answered: the run was interrupted, so it exits
// 1, not 4 (REF-165). The swept region sees the interrupt through its own
// context, as a real AWS call would.
func TestRunStatus_InterruptExitsOne(t *testing.T) {
	fakeAWSEnv(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	stubTwoRegions(t, func(rctx context.Context) error {
		cancel() // the user presses Ctrl+C while this region is swept
		<-rctx.Done()
		return rctx.Err()
	})
	// Two regions at once, so neither waits for a slot the other holds.
	err := runStatusCtx(ctx, t, "--max-concurrency", "2", "-r", "us-east-1", "-r", "eu-west-1", "-o", "json")
	if got := runner.ExitCodeOf(err); got != runner.ExitError || !strings.Contains(err.Error(), "interrupted") {
		t.Fatalf("exit = %d (%v), want 1 for the interrupt", got, err)
	}
}

// The --timeout deadline ending the sweep after one region answered is
// partial data: exit 4.
func TestRunStatus_DeadlineExitsIncomplete(t *testing.T) {
	fakeAWSEnv(t)
	stubTwoRegions(t, func(rctx context.Context) error {
		<-rctx.Done()
		return rctx.Err()
	})
	err := runStatusCtx(t.Context(), t, "--timeout", "200ms", "--max-concurrency", "2", "-r", "us-east-1", "-r", "eu-west-1", "-o", "json")
	if got := runner.ExitCodeOf(err); got != runner.ExitIncomplete {
		t.Fatalf("exit = %d (%v), want 4", got, err)
	}
}
