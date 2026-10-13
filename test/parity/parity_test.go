//go:build unix

package parity

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"go.uber.org/goleak"
)

var update = flag.Bool("update", false, "rewrite the golden traces in testdata")

// updateRuns is how often -update runs a scenario. Every run must give the
// same trace.
const updateRuns = 5

// maxConcurrency is the most requests a binary may have open at once: the
// default of the legacy --max-concurrency flag.
const maxConcurrency = 8

// legacy runs the legacy refresh binary.
var legacy Runner

// helperArg makes the test binary act as a stand-in for refresh. The runner
// tests use it for what the real binary does not do: ignore a signal, leave
// a child behind, print its environment.
const helperArg = "parity-helper"

// TestMain builds the legacy binary once. Set REFRESH_PARITY_LEGACY to the
// path of a binary to test that one and skip the build.
func TestMain(m *testing.M) {
	if len(os.Args) > 2 && os.Args[1] == helperArg {
		helper(os.Args[2])
		return
	}
	flag.Parse()
	dir, err := os.MkdirTemp("", "refresh-parity")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	legacy.Binary = os.Getenv("REFRESH_PARITY_LEGACY")
	if legacy.Binary == "" {
		legacy.Binary = filepath.Join(dir, "refresh-legacy")
		build := exec.Command("go", "build", "-o", legacy.Binary, "../../legacy") //nolint:gosec,noctx // fixed arguments; nothing to cancel it
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

// helper is the stand-in binary. It never returns to the tests.
func helper(mode string) {
	switch mode {
	case "env":
		for _, kv := range os.Environ() {
			fmt.Println(kv)
		}
	case "prompt":
		fmt.Print("Continue? [y/N] ")
		line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		fmt.Printf("answer=%q\n", line)
	case "sleep":
		signal.Ignore(os.Interrupt)
		select {}
	case "stubborn":
		// Ignore the interrupt, leave a child of its own behind, and wait
		// for an answer that never comes.
		signal.Ignore(os.Interrupt)
		child := exec.Command(os.Args[0], helperArg, "sleep") //nolint:gosec,noctx // the test binary itself
		child.Stdout = os.Stdout
		if err := child.Start(); err != nil {
			fmt.Println("error:", err)
			os.Exit(1)
		}
		fmt.Printf("grandchild %d\n", child.Process.Pid)
		req, _ := http.NewRequest(http.MethodGet, os.Getenv("AWS_ENDPOINT_URL")+"/clusters", nil) //nolint:noctx // it must hang
		req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=AKID/20260101/us-east-1/eks/aws4_request")
		_, _ = http.DefaultClient.Do(req) //nolint:bodyclose // no response ever arrives
		select {}
	}
	os.Exit(0)
}

// outputOnly are the trace fields no golden file holds.
var outputOnly = cmpopts.IgnoreFields(Trace{}, "Terminal", "PeakConcurrency")

// TestLegacyTraces runs every scenario's legacy command line, checks what
// the scenario means, and compares the trace with its golden file. Run with
// -update to rewrite the files.
func TestLegacyTraces(t *testing.T) {
	t.Parallel()
	for _, sc := range scenarios() {
		t.Run(sc.Name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join("testdata", "legacy", sc.Name+".json")
			if *update {
				writeGolden(t, path, record(t, sc))
				return
			}
			got := run(t, sc)
			if err := sc.check(got); err != nil {
				t.Errorf("the run does not show what the scenario is for:\n%v\n%s", err, brief(got))
			}
			// Peak concurrency depends on how the binary's parallel calls
			// overlap, which differs from run to run. Every run is held to
			// the binary's own limit.
			if got.PeakConcurrency > maxConcurrency {
				t.Errorf("peak concurrency = %d, want at most %d", got.PeakConcurrency, maxConcurrency)
			}
			var want Trace
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("%v (run the test with -update to record the trace)", err)
			}
			if err := json.Unmarshal(data, &want); err != nil {
				t.Fatalf("%s: %v", path, err)
			}
			if diff := cmp.Diff(want, roundTrip(t, got), outputOnly); diff != "" {
				t.Errorf("the trace differs from %s (-want +got):\n%s", path, diff)
			}
		})
	}
}

func run(t *testing.T, sc Scenario) Trace {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	trace, err := legacy.RunAfter(ctx, sc, sc.LegacyPreview, sc.Legacy, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if testing.Verbose() {
		t.Log(brief(trace))
	}
	return trace
}

// record runs the scenario updateRuns times and returns the trace. It fails
// if two runs differ, or if the trace does not show what the scenario is
// for.
func record(t *testing.T, sc Scenario) Trace {
	t.Helper()
	first := run(t, sc)
	if err := sc.check(first); err != nil {
		t.Fatalf("the run does not show what the scenario is for:\n%v\n%s", err, brief(first))
	}
	for i := 1; i < updateRuns; i++ {
		if diff := cmp.Diff(first, run(t, sc), outputOnly); diff != "" {
			t.Fatalf("run %d differs from run 1: the scenario is not deterministic (-first +this):\n%s", i+1, diff)
		}
	}
	return first
}

// brief describes a run for a failure message: its exit code, its update
// requests, and the end of what it printed.
func brief(t Trace) string {
	tail := func(s string) string {
		lines := strings.Split(strings.TrimSpace(s), "\n")
		return strings.Join(lines[max(0, len(lines)-8):], "\n")
	}
	return fmt.Sprintf("exit code %d; updates %v\nend of the terminal:\n%s\nend of stderr:\n%s", t.ExitCode, updates(t), tail(t.Terminal), tail(t.Stderr))
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

// TestMatrix holds the behaviour matrix to the scenarios: every cell names
// a scenario of that command and behaviour or says why there is none, and
// every scenario says what it is for.
func TestMatrix(t *testing.T) {
	t.Parallel()
	byName := map[string]Scenario{}
	for _, sc := range scenarios() {
		if _, dup := byName[sc.Name]; dup {
			t.Errorf("two scenarios are named %q", sc.Name)
		}
		byName[sc.Name] = sc
		switch {
		case len(sc.Legacy) == 0 || sc.Seed == nil:
			t.Errorf("%s needs a seed and a legacy command line", sc.Name)
		case sc.Behaviour == "" && len(sc.Checks) == 0:
			t.Errorf("%s has no behaviour and no checks: nothing says what it is for", sc.Name)
		case sc.Behaviour != "" && !slices.Contains(behaviours, sc.Behaviour):
			t.Errorf("%s has the unknown behaviour %q", sc.Name, sc.Behaviour)
		}
	}
	for _, command := range commands {
		for _, behaviour := range behaviours {
			c, ok := matrix[command][behaviour]
			switch {
			case !ok || (c.scenario == "") == (c.why == ""):
				t.Errorf("%s / %s: the cell needs a scenario or a reason, and not both", command, behaviour)
			case c.scenario != "":
				sc, found := byName[c.scenario]
				if !found || sc.Command != command || sc.Behaviour != behaviour {
					t.Errorf("%s / %s: scenario %q is not a %s scenario of that behaviour", command, behaviour, c.scenario, command)
				}
			}
		}
	}
}
