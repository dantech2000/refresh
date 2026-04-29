package ui

import (
	"context"
	"testing"
	"time"
)

func TestProgressSpinnerLifecycle(t *testing.T) {
	spinner := NewProgressSpinner("starting")
	if spinner == nil || spinner.message != "starting" {
		t.Fatalf("spinner = %#v", spinner)
	}
	stop := spinner.Start(context.Background())
	spinner.UpdateText("updated")
	spinner.Success("done")
	spinner.Fail("failed")
	spinner.Warning("warn")
	spinner.Stop("success")
	stop()

	NewPtermHealthSpinner("health").Stop("")
	NewProgressSpinner("empty").Stop("")
}

func TestProgressBarLifecycle(t *testing.T) {
	bar := NewProgressBar(2, "work")
	if err := bar.Start(); err != nil {
		t.Fatalf("Start() = %v", err)
	}
	bar.Increment()
	bar.Add(1)
	bar.UpdateTitle("done")
	bar.Stop()
}

func TestPerformanceTimer(t *testing.T) {
	timer := NewPerformanceTimer("operation")
	time.Sleep(time.Nanosecond)
	if timer.Elapsed() <= 0 {
		t.Fatal("expected positive elapsed time")
	}
	timer.PrintElapsed()
}

func TestFunMessages(t *testing.T) {
	fm := &FunMessages{
		Cluster:   []string{"cluster"},
		Nodegroup: []string{"nodegroup"},
		Addon:     []string{"addon"},
		General:   []string{"general"},
		Health:    []string{"health"},
	}

	for category, want := range map[string]string{
		"cluster":   "cluster",
		"nodegroup": "nodegroup",
		"addon":     "addon",
		"health":    "health",
		"other":     "general",
	} {
		got := fm.GetMessages(category)
		if len(got) != 1 || got[0] != want {
			t.Fatalf("GetMessages(%q) = %v, want %q", category, got, want)
		}
		if random := fm.GetRandomMessage(category); random != want {
			t.Fatalf("GetRandomMessage(%q) = %q, want %q", category, random, want)
		}
	}

	if got := (&FunMessages{}).GetRandomMessage("missing"); got != "Working on it..." {
		t.Fatalf("empty GetRandomMessage() = %q", got)
	}
}

func TestFunSpinnerLifecycle(t *testing.T) {
	oldInterval := funSpinnerInterval
	funSpinnerInterval = time.Millisecond
	t.Cleanup(func() { funSpinnerInterval = oldInterval })

	empty := NewFunSpinner(nil)
	if len(empty.messages) != 1 || empty.messages[0] != "Working on it..." {
		t.Fatalf("default messages = %v", empty.messages)
	}
	empty.Stop()

	spinner := NewFunSpinner([]string{"one", "two"})
	if err := spinner.Start(); err != nil {
		t.Fatalf("Start() = %v", err)
	}
	if err := spinner.Start(); err != nil {
		t.Fatalf("second Start() = %v", err)
	}
	time.Sleep(2 * time.Millisecond)
	spinner.Stop()
	spinner.Success("ok")
	spinner.Fail("bad")

	if NewFunSpinnerForCategory("cluster") == nil || NewEnhancedProgressSpinner("addon") == nil {
		t.Fatal("category spinners should not be nil")
	}
}

func TestMultiProgressAndRegionTracker(t *testing.T) {
	manager := NewMultiProgressManager()
	if manager.AddSpinner("spin") == nil || manager.AddProgressBar(1, "bar") == nil {
		t.Fatal("expected spinner and bar")
	}
	if err := manager.Start(); err != nil {
		t.Fatalf("manager Start() = %v", err)
	}
	manager.Stop()

	tracker := NewRegionProgressTracker([]string{"us-east-1", "us-west-2"}, "clusters")
	if tracker.IsComplete() {
		t.Fatal("tracker should not start complete")
	}
	if err := tracker.Start(); err != nil {
		t.Fatalf("tracker Start() = %v", err)
	}
	tracker.CompleteRegion("us-east-1", 1)
	tracker.CompleteRegion("us-west-2", 0)
	tracker.CompleteRegion("missing", 10)
	if !tracker.IsComplete() {
		t.Fatal("tracker should be complete")
	}
	tracker.Stop()
}
