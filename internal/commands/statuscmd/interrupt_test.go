package statuscmd

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/urfave/cli/v3"

	"github.com/dantech2000/refresh/internal/commands/runner"
	statussvc "github.com/dantech2000/refresh/internal/services/status"
)

// ctxRegion answers for us-east-1 and runs block for any other region.
type ctxRegion struct {
	region string
	block  func(ctx context.Context) error
}

func (f ctxRegion) ListClusterStatuses(ctx context.Context, _ statussvc.ListOptions) ([]statussvc.ClusterStatus, error) {
	if f.region == "us-east-1" {
		return []statussvc.ClusterStatus{{
			Name: "prod", Region: "us-east-1", Version: "1.33",
			Support: statussvc.SupportPosture{Tier: statussvc.SupportStandard},
		}}, nil
	}
	return nil, f.block(ctx)
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
// 1, not 4 (REF-165).
func TestRunStatus_InterruptExitsOne(t *testing.T) {
	fakeAWSEnv(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	stubRegionService(t, func(cfg aws.Config) regionLister {
		return ctxRegion{region: cfg.Region, block: func(context.Context) error {
			cancel() // the user presses Ctrl+C while this region is swept
			return context.Canceled
		}}
	})
	err := runStatusCtx(ctx, t, "--max-concurrency", "1", "-r", "us-east-1", "-r", "eu-west-1", "-o", "json")
	if got := runner.ExitCodeOf(err); got != runner.ExitError || !strings.Contains(err.Error(), "interrupted") {
		t.Fatalf("exit = %d (%v), want 1 for the interrupt", got, err)
	}
}

// The --timeout deadline ending the sweep after one region answered is
// partial data: exit 4.
func TestRunStatus_DeadlineExitsIncomplete(t *testing.T) {
	fakeAWSEnv(t)
	stubRegionService(t, func(cfg aws.Config) regionLister {
		return ctxRegion{region: cfg.Region, block: func(ctx context.Context) error {
			<-ctx.Done()
			return ctx.Err()
		}}
	})
	err := runStatusCtx(t.Context(), t, "--timeout", "200ms", "--max-concurrency", "1", "-r", "us-east-1", "-r", "eu-west-1", "-o", "json")
	if got := runner.ExitCodeOf(err); got != runner.ExitIncomplete {
		t.Fatalf("exit = %d (%v), want 4", got, err)
	}
}
