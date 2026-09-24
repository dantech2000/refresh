package nodegroup

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"reflect"
	"testing"

	"github.com/dantech2000/refresh/internal/health"
	"github.com/dantech2000/refresh/internal/mocks"
)

// A scale with no size would start an EKS update that changes nothing, so
// Scale refuses it before any call, dry run or not.
func TestScale_NoSizeRequested(t *testing.T) {
	for _, dryRun := range []bool{false, true} {
		api := mocks.NewEKSAPI().WithCluster("prod", "1.32").Build()
		svc := newTestService(api)
		err := svc.Scale(context.Background(), "prod", "workers", nil, nil, nil, ScaleOptions{DryRun: dryRun})
		if !errors.Is(err, ErrNoScaleChange) {
			t.Errorf("dryRun=%v: err = %v, want ErrNoScaleChange", dryRun, err)
		}
		if api.Calls.UpdateNodegroupConfig != 0 {
			t.Errorf("dryRun=%v: UpdateNodegroupConfig called %d times, want 0", dryRun, api.Calls.UpdateNodegroupConfig)
		}
	}
}

// Health warnings go to OnHealthWarnings when it is set, so the command can
// print them after its spinner; they reach the logger only without it.
func TestScale_ReportHealthWarnings(t *testing.T) {
	var logged bytes.Buffer
	svc := &ServiceImpl{logger: slog.New(slog.NewTextHandler(&logged, nil))}
	warn := health.HealthSummary{Decision: health.DecisionWarn, Warnings: []string{"node ip-1 has DiskPressure"}}

	var gotStage string
	var got []string
	opts := ScaleOptions{OnHealthWarnings: func(stage string, w []string) { gotStage, got = stage, w }}
	svc.reportHealthWarnings(opts, "pre-scaling", warn)
	if gotStage != "pre-scaling" || !reflect.DeepEqual(got, warn.Warnings) {
		t.Errorf("callback got (%q, %v), want (pre-scaling, %v)", gotStage, got, warn.Warnings)
	}
	if logged.Len() != 0 {
		t.Errorf("logged with a callback set:\n%s", logged.String())
	}

	got = nil
	svc.reportHealthWarnings(opts, "post-scaling", health.HealthSummary{Decision: health.DecisionProceed, Warnings: []string{"ignored"}})
	if got != nil {
		t.Errorf("a PROCEED verdict reported warnings %v", got)
	}

	svc.reportHealthWarnings(ScaleOptions{}, "post-scaling", warn)
	if !bytes.Contains(logged.Bytes(), []byte("post-scaling health warnings")) {
		t.Errorf("without a callback the warnings are not logged:\n%s", logged.String())
	}
}
