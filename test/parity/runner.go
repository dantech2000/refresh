// Package parity runs a refresh binary as a subprocess against a fake fleet
// and records what it did: its output, its exit code, the AWS requests that
// change state, and the state of the fleet afterwards.
//
// A Scenario pairs a legacy command line with its v2 equivalent on the same
// world. Two traces of one scenario must agree on the mutating requests and
// the exit code; see "Parity suite" in REWRITE.md.
//
// # Time
//
// The world's clock is virtual, and the binary's is real. The runner ties
// them with one rule: each time a request for one of the scenario's clock
// operations arrives, the world moves forward by the scenario's step before
// the request is answered. The binary polls a running update, each poll
// moves the world, and after a fixed number of polls the update ends. No
// part of a scenario waits for wall-clock time except the binary's own poll
// ticker, which the scenario sets as low as the command allows.
//
// # Signals
//
// A scenario can send a signal when the Nth request of an operation arrives.
// The runner sends it from the server's request hook and then holds that
// request until the binary drops it, so the binary always sees the signal
// with the same request unanswered.
package parity

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"maps"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/dantech2000/refresh/internal/awsx/awsxtest"
	"github.com/dantech2000/refresh/internal/fleet/fleettest"
)

// Region is the region every scenario runs in.
const Region = "us-east-1"

// Scenario is one command line run on one world.
type Scenario struct {
	Name string
	// Seed fills the world. Faults, when set, runs after it.
	Seed   func(s *fleettest.State)
	Faults func(w *fleettest.World)
	// Legacy and V2 are the arguments of the legacy binary and of the v2
	// binary for the same intent. An empty one means that binary has no
	// such run yet.
	Legacy []string
	V2     []string
	// Stdin is what the binary reads. It is never a terminal.
	Stdin string
	// Step is how far the world's clock moves for each request of a clock
	// operation. Zero means one minute.
	Step time.Duration
	// Clock lists the operations that move the clock, in place of the
	// default: DescribeUpdate and DescribeInsightsRefresh, which only wait
	// loops call.
	Clock []fleettest.Operation
	// Signal, when set, is sent to the binary once.
	Signal *Signal
	// Cluster is the cluster whose final state the trace records. Empty
	// means "prod".
	Cluster string
	// UnstableReads are read operations whose request count depends on
	// timing in the binary, such as a watcher on its own ticker. The trace
	// records that they were called, not how often.
	UnstableReads []fleettest.Operation
}

// Signal says when to interrupt the binary.
type Signal struct {
	// Signal is sent when the Nth request that Match accepts arrives,
	// counted from 1.
	Signal os.Signal
	Match  func(r awsxtest.Request) bool
	Nth    int
}

// Trace is what one run did. Everything in it is normalised (see
// normalise), so two runs of a deterministic binary give equal traces.
type Trace struct {
	ExitCode int    `json:"exitCode"`
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	// Mutations are the requests that change AWS state, in arrival order.
	Mutations []Mutation `json:"mutations"`
	// Reads counts the other requests by operation. An operation in the
	// scenario's UnstableReads has the count -1.
	Reads map[fleettest.Operation]int `json:"reads"`
	// PeakConcurrency is the most requests the binary had open at once. It
	// depends on how parallel calls overlap, so it differs between runs of
	// one binary: hold it to a limit, do not compare it for equality.
	PeakConcurrency int `json:"peakConcurrency"`
	// World is the state of the scenario's cluster after the run.
	World ClusterState `json:"world"`
}

// Mutation is one request that changes AWS state.
type Mutation struct {
	Operation fleettest.Operation `json:"operation"`
	Region    string              `json:"region"`
	// Target is the resource: "cluster", "cluster/nodegroup", or
	// "cluster/addon".
	Target string `json:"target"`
	// Payload is the request's input without the fields that name the
	// target. A client request token is renamed token-1, token-2, and so
	// on by first use in the run, so two requests with the same token
	// still show the same name.
	Payload map[string]any `json:"payload"`
}

// ClusterState is the part of a cluster a change can touch.
type ClusterState struct {
	Version    string            `json:"version"`
	Status     string            `json:"status"`
	Nodegroups map[string]string `json:"nodegroups"`
	Addons     map[string]string `json:"addons"`
	// Updates are the updates started during or before the run, oldest
	// first.
	Updates []string `json:"updates"`
}

// Run runs binary with args on a fresh world for the scenario and returns
// the trace. dir is an empty directory for the run's home and config.
func Run(ctx context.Context, sc Scenario, binary string, args []string, dir string) (Trace, error) {
	world := fleettest.New()
	world.Edit(sc.Seed)
	if sc.Faults != nil {
		sc.Faults(world)
	}
	// The process does not exist yet when the hook is installed, and the
	// hook must be installed before the server starts. started carries the
	// order: the hook reads cmd.Process only after Start returned.
	started := make(chan struct{})
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, binary, args...) //nolint:gosec // the binary under test, built by the test
	driver := &driver{sc: sc, world: world, signal: func(sig os.Signal) error {
		<-started
		return cmd.Process.Signal(sig)
	}}
	handler := awsxtest.NewHandler(world)
	handler.OnRequest = driver.onRequest
	server := httptest.NewServer(handler)
	defer server.Close()
	defer handler.Close() // first: it releases a request the driver still holds

	empty := filepath.Join(dir, "empty")
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		return Trace{}, err
	}
	cmd.Dir = dir
	cmd.Env = environment(server.URL, dir, empty)
	cmd.Stdin = strings.NewReader(sc.Stdin)
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	// A cancelled run gets a chance to print what it was doing.
	cmd.Cancel = func() error { return cmd.Process.Signal(os.Interrupt) }
	cmd.WaitDelay = 5 * time.Second

	if err := cmd.Start(); err != nil {
		return Trace{}, fmt.Errorf("starting %s: %w", binary, err)
	}
	close(started)
	err := cmd.Wait()
	var exit *exec.ExitError
	if err != nil && !errors.As(err, &exit) {
		return Trace{}, fmt.Errorf("running %s %s: %w\nstderr:\n%s", binary, strings.Join(args, " "), err, stderr.String())
	}
	if ctx.Err() != nil {
		return Trace{}, fmt.Errorf("%s %s did not finish: %w\nstdout:\n%s\nstderr:\n%s", binary, strings.Join(args, " "), ctx.Err(), stdout.String(), stderr.String())
	}
	if err := driver.err(); err != nil {
		return Trace{}, err
	}

	n := newNormaliser(server.URL, dir)
	trace := Trace{
		ExitCode:        cmd.ProcessState.ExitCode(),
		Mutations:       []Mutation{},
		Reads:           map[fleettest.Operation]int{},
		PeakConcurrency: handler.PeakConcurrency("", ""),
		World:           clusterState(world, orDefault(sc.Cluster, "prod")),
	}
	for _, r := range handler.Requests() {
		switch {
		case r.Mutates:
			trace.Mutations = append(trace.Mutations, n.mutation(r))
		case slices.Contains(sc.UnstableReads, r.Operation):
			trace.Reads[r.Operation] = -1
		default:
			trace.Reads[r.Operation]++
		}
	}
	// The output is normalised after the requests, so a token it prints has
	// the name its request gave it.
	trace.Stdout, trace.Stderr = n.text(stdout.String()), n.text(stderr.String())
	return trace, nil
}

func orDefault(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

// environment is everything the binary sees: the fake endpoint, static
// credentials, and empty config. Nothing is inherited, so no AWS_* or
// REFRESH_* variable, profile, or kubeconfig of the machine can reach it.
func environment(url, dir, emptyFile string) []string {
	return []string{
		"HOME=" + dir,
		"XDG_CONFIG_HOME=" + filepath.Join(dir, "config"),
		"XDG_CACHE_HOME=" + filepath.Join(dir, "cache"),
		"TMPDIR=" + dir,
		"PATH=/usr/bin:/bin",
		"TZ=UTC",
		"LANG=C",
		"TERM=dumb",
		"NO_COLOR=1",
		"COLUMNS=120",
		"LINES=40",
		"AWS_ENDPOINT_URL=" + url,
		"AWS_ACCESS_KEY_ID=AKIDFAKEFAKEFAKE",
		"AWS_SECRET_ACCESS_KEY=fake-secret",
		"AWS_REGION=" + Region,
		"AWS_CONFIG_FILE=" + emptyFile,
		"AWS_SHARED_CREDENTIALS_FILE=" + emptyFile,
		"AWS_EC2_METADATA_DISABLED=true",
		"KUBECONFIG=" + emptyFile,
		"REFRESH_CONFIG_HOME=" + filepath.Join(dir, "refresh"),
	}
}

// defaultClock are the operations only a wait loop calls.
var defaultClock = []fleettest.Operation{fleettest.EKSDescribeUpdate, fleettest.EKSDescribeInsightsRefresh}

// driver moves the world's clock and sends the scenario's signal as the
// binary's requests arrive.
type driver struct {
	sc     Scenario
	world  *fleettest.World
	signal func(os.Signal) error

	mu      sync.Mutex
	matched int // requests the scenario's Signal matched
	failure error
}

func (d *driver) onRequest(ctx context.Context, r awsxtest.Request) {
	clock := d.sc.Clock
	if clock == nil {
		clock = defaultClock
	}
	if slices.Contains(clock, r.Operation) {
		step := d.sc.Step
		if step == 0 {
			step = time.Minute
		}
		d.world.Advance(step)
	}

	s := d.sc.Signal
	if s == nil || !s.Match(r) {
		return
	}
	d.mu.Lock()
	d.matched++
	n := d.matched
	d.mu.Unlock()
	if n == s.Nth {
		if err := d.signal(s.Signal); err != nil {
			d.mu.Lock()
			d.failure = fmt.Errorf("sending %s: %w", s.Signal, err)
			d.mu.Unlock()
			return
		}
		// Hold the request until the binary gives it up, so the signal
		// always lands with this request unanswered.
		<-ctx.Done()
	}
}

func (d *driver) err() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.failure != nil {
		return d.failure
	}
	if s := d.sc.Signal; s != nil && d.matched < s.Nth {
		return fmt.Errorf("the signal was never sent: %d matching requests arrived, want %d", d.matched, s.Nth)
	}
	return nil
}

// clusterState reads the final state of a cluster. A cluster that is not in
// the world gives the zero state.
func clusterState(w *fleettest.World, name string) ClusterState {
	out := ClusterState{Nodegroups: map[string]string{}, Addons: map[string]string{}, Updates: []string{}}
	w.View(func(s *fleettest.State) {
		c := s.Cluster(Region, name)
		if c == nil {
			return
		}
		out.Version, out.Status = c.Version, string(c.Status)
		for _, ng := range c.Nodegroups {
			out.Nodegroups[ng.Name] = fmt.Sprintf("%s %s %s %d/%d/%d", ng.Version, ng.ReleaseVersion, ng.Status, ng.Desired, ng.Min, ng.Max)
		}
		for _, a := range c.Addons {
			out.Addons[a.Name] = strings.TrimSpace(fmt.Sprintf("%s %s %s", a.Version, a.Status, a.ConfigurationValues))
		}
		for _, u := range c.Updates {
			var params []string
			for _, p := range u.Params {
				params = append(params, p.Type+"="+p.Value)
			}
			target := "cluster"
			if u.Nodegroup+u.Addon != "" {
				target = u.Nodegroup + u.Addon
			}
			line := fmt.Sprintf("%s %s %s %s", target, u.Type, u.Status, strings.Join(params, ","))
			if u.Force {
				line += " force"
			}
			if u.RollbackTimeoutMinutes != 0 {
				line += fmt.Sprintf(" rollbackTimeout=%dm", u.RollbackTimeoutMinutes)
			}
			out.Updates = append(out.Updates, line)
		}
	})
	return out
}

// normaliser replaces what differs between two runs of the same scenario.
// The list is:
//
//   - the server's address, which has a random port: "http://fake"
//   - the run's directory: "$DIR"
//   - client request tokens: "token-N", numbered by first use in a request
//   - durations the binary measured on the real clock, such as "1.2s" or
//     "350ms": "<duration>"
//   - dates, times of day, and timestamps, which come from the real clock
//     or are compared with it: "<time>"
//
// Update IDs are not on the list: the world numbers them.
type normaliser struct {
	url, dir string
	tokens   map[string]string
}

func newNormaliser(url, dir string) *normaliser {
	return &normaliser{url: url, dir: dir, tokens: map[string]string{}}
}

func (n *normaliser) token(t string) string {
	if _, ok := n.tokens[t]; !ok {
		n.tokens[t] = fmt.Sprintf("token-%d", len(n.tokens)+1)
	}
	return n.tokens[t]
}

// targetKeys are the input fields that name the target of a mutation.
var targetKeys = []string{"name", "clusterName", "nodegroupName", "addonName"}

func (n *normaliser) mutation(r awsxtest.Request) Mutation {
	m := Mutation{Operation: r.Operation, Region: r.Region, Payload: maps.Clone(r.Input)}
	for _, key := range targetKeys {
		if v, ok := r.Input[key].(string); ok {
			m.Target = strings.TrimPrefix(m.Target+"/"+v, "/")
			delete(m.Payload, key)
		}
	}
	if t, ok := m.Payload["clientRequestToken"].(string); ok {
		m.Payload["clientRequestToken"] = n.token(t)
	}
	return m
}

var (
	// realDuration matches a Go duration with a fraction or a sub-second
	// unit, which only a measurement on the real clock prints. Whole
	// seconds, minutes, and hours ("5m0s", "15s") are flag values and stay.
	realDuration = regexp.MustCompile(`\b\d+(\.\d+)?(ns|µs|us|ms)\b|\b\d+\.\d+s\b`)
	// realTime matches an RFC 3339 timestamp, a time of day, and a date.
	realTime = regexp.MustCompile(`\b\d{4}-\d{2}-\d{2}([T ]\d{2}:\d{2}:\d{2}(\.\d+)?(Z|[+-]\d{2}:\d{2})?)?|\b\d{2}:\d{2}:\d{2}\b`)
)

func (n *normaliser) text(s string) string {
	s = strings.ReplaceAll(s, n.url, "http://fake")
	s = strings.ReplaceAll(s, n.dir, "$DIR")
	// Longest first, so a token that contains another is replaced whole.
	tokens := slices.SortedFunc(maps.Keys(n.tokens), func(a, b string) int { return len(b) - len(a) })
	for _, t := range tokens {
		s = strings.ReplaceAll(s, t, n.tokens[t])
	}
	s = realTime.ReplaceAllString(s, "<time>")
	return realDuration.ReplaceAllString(s, "<duration>")
}
