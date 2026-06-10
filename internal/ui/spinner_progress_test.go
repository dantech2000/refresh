package ui

import (
	"testing"
	"time"
)

func TestFunMessages(t *testing.T) {
	fm := &FunMessages{
		Cluster:   []string{"cluster"},
		Nodegroup: []string{"nodegroup"},
		Addon:     []string{"addon"},
		General:   []string{"general"},
		Health:    []string{"health"},
		Workload:  []string{"workload"},
	}

	for category, want := range map[string]string{
		"cluster":   "cluster",
		"nodegroup": "nodegroup",
		"addon":     "addon",
		"health":    "health",
		"workload":  "workload",
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
