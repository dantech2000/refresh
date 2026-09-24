package ui

import (
	"bytes"
	"io"
	"strings"
	"testing"
	"time"

	"go.uber.org/goleak"
)

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
	}
}

func TestFunSpinnerLifecycle(t *testing.T) {
	defer goleak.VerifyNone(t)
	oldInterval := funSpinnerInterval
	funSpinnerInterval = time.Millisecond
	t.Cleanup(func() { funSpinnerInterval = oldInterval })

	// Force the animated path: tests run without a TTY.
	oldTTY := spinnerOutputIsTerminal
	spinnerOutputIsTerminal = func() bool { return true }
	t.Cleanup(func() { spinnerOutputIsTerminal = oldTTY })

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

	if NewFunSpinnerForCategory("cluster") == nil {
		t.Fatal("category spinners should not be nil")
	}
}

// Spinner frames and the Success line must go to the spinner stream
// (stderr), never to stdout: pterm's default writer is stdout, and a stray
// "SUCCESS" line there would break -o json/yaml output.
func TestFunSpinnerWritesToSpinnerStream(t *testing.T) {
	oldTTY, oldOut := spinnerOutputIsTerminal, spinnerOut
	t.Cleanup(func() { spinnerOutputIsTerminal, spinnerOut = oldTTY, oldOut })
	spinnerOutputIsTerminal = func() bool { return true }
	var buf bytes.Buffer
	spinnerOut = func() io.Writer { return &buf }

	stdout := captureStdout(t, func() {
		spinner := NewFunSpinner([]string{"working"})
		if err := spinner.Start(); err != nil {
			t.Fatalf("Start() = %v", err)
		}
		spinner.Success("done-marker")
	})
	if stdout != "" {
		t.Errorf("spinner wrote to stdout: %q", stdout)
	}
	if !strings.Contains(buf.String(), "done-marker") {
		t.Errorf("spinner stream missing the success line; got %q", buf.String())
	}
}

func TestNewFunSpinnerDefaultsToStderr(t *testing.T) {
	if got := NewFunSpinner(nil).spinner.Writer; got != Stderr {
		t.Errorf("spinner writer = %v, want ui.Stderr", got)
	}
}

func TestFunSpinnerNonInteractiveStaysSilent(t *testing.T) {
	oldTTY, oldOut := spinnerOutputIsTerminal, spinnerOut
	spinnerOutputIsTerminal = func() bool { return false }
	var buf bytes.Buffer
	spinnerOut = func() io.Writer { return &buf }
	t.Cleanup(func() { spinnerOutputIsTerminal, spinnerOut = oldTTY, oldOut })

	spinner := NewFunSpinner([]string{"msg"})
	if err := spinner.Start(); err != nil {
		t.Fatalf("Start() = %v", err)
	}
	if spinner.animated {
		t.Fatal("spinner must not animate when output is not a terminal")
	}
	// Stop must not deadlock waiting for a render goroutine that never started.
	spinner.Stop()
	spinner.Success("ok")
	if buf.Len() != 0 {
		t.Errorf("spinner wrote to a non-terminal stderr: %q", buf.String())
	}
}
