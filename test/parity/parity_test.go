package parity

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"go.uber.org/goleak"
)

var update = flag.Bool("update", false, "rewrite the golden traces in testdata")

// updateRuns is how often -update runs a scenario. Every run must give the
// same trace, and the golden file keeps the highest peak concurrency seen.
const updateRuns = 5

// maxConcurrency is the most requests a binary may have open at once: the
// default of the legacy --max-concurrency flag.
const maxConcurrency = 8

// legacyBinary is the legacy refresh binary under test.
var legacyBinary string

// TestMain builds the legacy binary once. Set REFRESH_PARITY_LEGACY to the
// path of a binary to test that one and skip the build.
func TestMain(m *testing.M) {
	flag.Parse()
	dir, err := os.MkdirTemp("", "refresh-parity")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	legacyBinary = os.Getenv("REFRESH_PARITY_LEGACY")
	if legacyBinary == "" {
		legacyBinary = filepath.Join(dir, "refresh-legacy")
		build := exec.Command("go", "build", "-o", legacyBinary, "../../legacy") //nolint:gosec,noctx // fixed arguments; nothing to cancel it
		build.Stderr = os.Stderr
		if err := build.Run(); err != nil {
			fmt.Fprintln(os.Stderr, "building the legacy binary:", err)
			_ = os.RemoveAll(dir)
			os.Exit(1)
		}
	}
	goleak.VerifyTestMain(m, goleak.Cleanup(func(code int) {
		_ = os.RemoveAll(dir)
		os.Exit(code)
	}))
}

// TestLegacyTraces runs every scenario's legacy command line and compares
// the trace with its golden file. Run with -update to rewrite the files.
func TestLegacyTraces(t *testing.T) {
	t.Parallel()
	seen := map[string]bool{}
	for _, sc := range scenarios() {
		if seen[sc.Name] {
			t.Fatalf("two scenarios are named %q", sc.Name)
		}
		seen[sc.Name] = true
		t.Run(sc.Name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join("testdata", "legacy", sc.Name+".json")
			if *update {
				writeGolden(t, path, record(t, sc))
				return
			}
			got := run(t, sc)
			var want Trace
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("%v (run the test with -update to record the trace)", err)
			}
			if err := json.Unmarshal(data, &want); err != nil {
				t.Fatalf("%s: %v", path, err)
			}
			// Peak concurrency depends on how the binary's parallel calls
			// overlap, which differs from run to run. The golden file keeps
			// the highest value its recording saw, for the reader. The test
			// holds every run to the binary's own limit.
			if got.PeakConcurrency > maxConcurrency {
				t.Errorf("peak concurrency = %d, want at most %d", got.PeakConcurrency, maxConcurrency)
			}
			got.PeakConcurrency = want.PeakConcurrency
			if diff := cmp.Diff(want, roundTrip(t, got)); diff != "" {
				t.Errorf("the trace differs from %s (-want +got):\n%s", path, diff)
			}
		})
	}
}

func run(t *testing.T, sc Scenario) Trace {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	trace, err := Run(ctx, sc, legacyBinary, sc.Legacy, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return trace
}

// record runs the scenario updateRuns times and returns the trace, with the
// highest peak concurrency of the runs. It fails if two runs differ in
// anything else.
func record(t *testing.T, sc Scenario) Trace {
	t.Helper()
	first := run(t, sc)
	for i := 1; i < updateRuns; i++ {
		next := run(t, sc)
		first.PeakConcurrency = max(first.PeakConcurrency, next.PeakConcurrency)
		next.PeakConcurrency = first.PeakConcurrency
		if diff := cmp.Diff(first, next); diff != "" {
			t.Fatalf("run %d differs from run 1: the scenario is not deterministic (-first +this):\n%s", i+1, diff)
		}
	}
	return first
}

func writeGolden(t *testing.T, path string, trace Trace) {
	t.Helper()
	data, err := json.MarshalIndent(trace, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
}

// roundTrip passes a trace through JSON, so its payloads have the types a
// golden file decodes to.
func roundTrip(t *testing.T, trace Trace) Trace {
	t.Helper()
	data, err := json.Marshal(trace)
	if err != nil {
		t.Fatal(err)
	}
	var out Trace
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	return out
}
