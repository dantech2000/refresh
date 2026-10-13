//go:build unix

// Package parity runs a refresh binary as a subprocess against a fake fleet
// and records what it did to the fleet: the requests that change state, what
// the binary had read about a resource before each of them, and the state of
// the fleet afterwards.
//
// The old and the new CLI have different commands, flags, documents, and
// messages, so their output is not compared. What the old binary remains the
// reference for is safety: which changes it makes, in what order, after
// which reads, and when it refuses. A Scenario pairs a legacy command line
// with its v2 equivalent on the same world, and Compare holds the two traces
// to each other on those points. See "Parity suite" in REWRITE.md.
//
// # Time
//
// The world's clock is virtual, and the binary's is real. The runner ties
// them with one rule. A poll is a request for one of the scenario's clock
// operations, such as DescribeUpdate. When a poll arrives for something the
// binary has already polled since the clock last moved, a new round of
// polls has begun, and the world moves forward by the scenario's step
// before that poll is answered. So every poll of one round reads the same
// world time, whatever order a binary that waits for several updates at
// once sends them in, and an update ends after a fixed number of rounds. No
// part of a scenario waits for wall-clock time except the binary's own poll
// ticker and deadlines, which the scenario sets as low as the command
// allows.
//
// # Signals and terminals
//
// A scenario can send a signal when the Nth request that matches a rule
// arrives. The runner sends it from the server's request hook and then holds
// that request until the binary drops it, so the binary always sees the
// signal with the same request unanswered.
//
// A scenario with a Terminal runs the binary on a pseudo-terminal and
// answers its prompts: each time the expected text appears, the runner
// types the next reply.
package parity

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"

	"github.com/dantech2000/refresh/internal/awsx/awsxtest"
	"github.com/dantech2000/refresh/internal/fleet/fleettest"
	"github.com/dantech2000/refresh/internal/kube/kubetest"
)

// Region is the region the scenarios run in.
const Region = "us-east-1"

// Runner runs scenarios against one binary.
type Runner struct {
	// Binary is the path of the refresh binary.
	Binary string
	// Grace is how long a binary gets to stop after the runner interrupts
	// it because the run's context ended. After that its whole process
	// group is killed. Zero means five seconds.
	Grace time.Duration
}

// RunError is the error of a run that did not produce a trace. It carries
// what the binary printed until then.
type RunError struct {
	Err    error
	Stdout string
	Stderr string
}

func (e *RunError) Error() string {
	return fmt.Sprintf("%v\nstdout:\n%s\nstderr:\n%s", e.Err, e.Stdout, e.Stderr)
}

func (e *RunError) Unwrap() error { return e.Err }

// Run runs the binary with args on a fresh world for the scenario and
// returns the trace. dir is an empty directory for the run's home and
// config.
func (r Runner) Run(ctx context.Context, sc Scenario, args []string, dir string) (Trace, error) {
	return r.RunAfter(ctx, sc, nil, args, dir)
}

// RunAfter is Run for a scenario with a preview: it first runs the binary
// with the preview arguments (a dry run), then changes the world as the
// scenario's AfterPreview says, and then runs it with args. The trace is
// that of the second run. A nil preview runs args alone.
func (r Runner) RunAfter(ctx context.Context, sc Scenario, preview, args []string, dir string) (Trace, error) {
	world := fleettest.New()
	world.Edit(sc.Seed)
	if sc.Prepare != nil {
		if err := sc.Prepare(world); err != nil {
			return Trace{}, fmt.Errorf("preparing the world: %w", err)
		}
	}

	// The process does not exist yet when the hooks are installed, and the
	// hooks must be installed before the server starts. started carries
	// the order: a hook reads cmd.Process only after Start returned.
	started := make(chan struct{})
	cmd := exec.Command(r.Binary, args...) //nolint:gosec,noctx // the binary under test; run stops it with its own grace period
	rec := &recorder{sc: sc, world: world, signal: func(sig os.Signal) error {
		<-started
		return cmd.Process.Signal(sig)
	}}
	rec.paused = preview != nil
	handler := awsxtest.NewHandler(world)
	handler.OnRequest, handler.OnAnswer = rec.onRequest, rec.onAnswer
	server := httptest.NewServer(handler)
	defer server.Close()
	defer handler.Close() // first: it releases a request the recorder still holds

	empty := filepath.Join(dir, "empty")
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		return Trace{}, err
	}
	kubeconfig := empty
	if sc.Kube {
		// The cluster's Kubernetes API. NewServer makes it the endpoint the
		// fake EKS reports, and the kubeconfig names the same server: the
		// binary uses a kubeconfig context only when the two match.
		kube := kubetest.NewHandler(world, Region, cmp.Or(sc.Cluster, "prod"))
		kube.OnRequest, kube.OnAnswer = rec.onKubeRequest, rec.onKubeAnswer
		kubeServer := kubetest.Serve(kube)
		defer kubeServer.Close()
		kubeconfig = filepath.Join(dir, "kubeconfig")
		if err := kubeServer.WriteKubeconfig(kubeconfig); err != nil {
			return Trace{}, err
		}
	}
	env := environment(server.URL, dir, empty, kubeconfig)
	if preview != nil {
		dry := exec.Command(r.Binary, preview...) //nolint:gosec,noctx // the binary under test
		dry.Dir, dry.Env = dir, env
		previewStarted := make(chan struct{})
		if out, err := r.run(ctx, dry, Scenario{}, previewStarted); err != nil {
			return Trace{}, &RunError{Err: fmt.Errorf("preview %s %s: %w", r.Binary, strings.Join(preview, " "), err), Stdout: out.stdout, Stderr: out.stderr}
		}
		if sc.AfterPreview != nil {
			if err := sc.AfterPreview(world); err != nil {
				return Trace{}, fmt.Errorf("changing the world after the preview: %w", err)
			}
		}
		rec.resume()
	}
	cmd.Dir = dir
	cmd.Env = env
	startedAt := time.Now()

	out, err := r.run(ctx, cmd, sc, started)
	if err != nil {
		return Trace{}, &RunError{Err: fmt.Errorf("%s %s: %w", r.Binary, strings.Join(args, " "), err), Stdout: out.stdout + out.terminal, Stderr: out.stderr}
	}
	if err := rec.err(); err != nil {
		return Trace{}, &RunError{Err: err, Stdout: out.stdout + out.terminal, Stderr: out.stderr}
	}

	n := newNormaliser(server.URL, dir, startedAt)
	trace := buildTrace(sc, n, rec, world)
	trace.ExitCode = cmd.ProcessState.ExitCode()
	trace.RefusedBeforeMutation = trace.ExitCode != 0 && len(trace.Mutations()) == 0
	trace.PeakConcurrency = handler.PeakConcurrency("", "")
	// The output is normalised after the requests, so a token it prints has
	// the name its request gave it.
	trace.Stdout, trace.Stderr, trace.Terminal = n.text(out.stdout), n.text(out.stderr), n.text(out.terminal)
	if sc.Live {
		// The live view prints a snapshot whenever its polls see a change.
		trace.Terminal, trace.Stdout, trace.Stderr = trace.Stdout+trace.Stderr, "", ""
	}
	return trace, nil
}

// output is what the binary wrote: stdout and stderr, or the transcript of
// its terminal.
type output struct {
	stdout, stderr, terminal string
}

// run starts cmd, waits for it, and stops it when ctx ends. The binary runs
// in its own process group, and whatever is left of the group is killed
// before run returns.
func (r Runner) run(ctx context.Context, cmd *exec.Cmd, sc Scenario, started chan<- struct{}) (output, error) {
	grace := r.Grace
	if grace == 0 {
		grace = 5 * time.Second
	}
	var stdout, stderr bytes.Buffer
	var term *terminal
	if sc.Terminal != nil {
		var err error
		if term, err = openTerminal(cmd, *sc.Terminal); err != nil {
			return output{}, err
		}
	} else {
		cmd.Stdin = strings.NewReader(sc.Stdin)
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		// A binary that exits and leaves a child holding its pipes must
		// not hang the run.
		cmd.WaitDelay = grace
	}
	if err := cmd.Start(); err != nil {
		if term != nil {
			term.close()
		}
		return output{}, fmt.Errorf("starting: %w", err)
	}
	close(started)
	// With Setpgid or Setsid the process leads a group with its own PID.
	group := cmd.Process.Pid
	if term != nil {
		term.start()
	}

	// stopper interrupts the binary when ctx ends, and kills its group if
	// that does not end it.
	exited := make(chan struct{})
	var stopper sync.WaitGroup
	stopper.Go(func() {
		select {
		case <-exited:
			return
		case <-ctx.Done():
		}
		_ = cmd.Process.Signal(os.Interrupt)
		timer := time.NewTimer(grace)
		defer timer.Stop()
		select {
		case <-exited:
		case <-timer.C:
			_ = syscall.Kill(-group, syscall.SIGKILL)
		}
	})
	waitErr := cmd.Wait()
	close(exited)
	stopper.Wait()
	// Whatever the binary started and left behind goes with it.
	_ = syscall.Kill(-group, syscall.SIGKILL)

	out := output{stdout: stdout.String(), stderr: stderr.String()}
	if term != nil {
		out.terminal = term.finish()
	}
	var exit *exec.ExitError
	switch {
	case ctx.Err() != nil:
		return out, fmt.Errorf("did not finish: %w", ctx.Err())
	case waitErr != nil && !errors.As(waitErr, &exit):
		return out, waitErr
	case cmd.ProcessState.ExitCode() < 0:
		return out, fmt.Errorf("ended by %s", cmd.ProcessState)
	case term != nil && term.err != nil:
		return out, term.err
	}
	return out, nil
}

// environment is everything the binary sees: the fake endpoint, static
// credentials, and empty config. Nothing is inherited, so no AWS_* or
// REFRESH_* variable, proxy, profile, or kubeconfig of the machine can
// reach it.
func environment(url, dir, emptyFile, kubeconfig string) []string {
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
		"KUBECONFIG=" + kubeconfig,
		"REFRESH_CONFIG_HOME=" + filepath.Join(dir, "refresh"),
	}
}

// Terminal says how to answer the prompts of a binary that runs on a
// pseudo-terminal.
type Terminal struct {
	// Expect is the text that marks a prompt, such as "[y/N]".
	Expect string
	// Replies are typed in order, one for each time Expect appears. The
	// last one repeats. "\x03" is Ctrl+C.
	Replies []string
}

// terminal is the runner's end of the binary's pseudo-terminal.
type terminal struct {
	script   Terminal
	ptmx     *os.File
	tty      *os.File
	done     chan struct{}
	text     bytes.Buffer
	answered int
	err      error
}

// openTerminal gives cmd a pseudo-terminal of 120 by 40 as its stdin,
// stdout, and stderr, and makes it the controlling terminal of a new
// session, so that Ctrl+C typed into it interrupts the binary.
func openTerminal(cmd *exec.Cmd, script Terminal) (*terminal, error) {
	ptmx, tty, err := pty.Open()
	if err != nil {
		return nil, fmt.Errorf("opening a pseudo-terminal: %w", err)
	}
	if err := pty.Setsize(ptmx, &pty.Winsize{Rows: 40, Cols: 120}); err != nil {
		_ = ptmx.Close()
		_ = tty.Close()
		return nil, fmt.Errorf("sizing the pseudo-terminal: %w", err)
	}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = tty, tty, tty
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
	return &terminal{script: script, ptmx: ptmx, tty: tty, done: make(chan struct{})}, nil
}

func (t *terminal) close() {
	_ = t.tty.Close()
	_ = t.ptmx.Close()
}

// start reads what the binary prints and types the replies. The caller has
// started the binary.
func (t *terminal) start() {
	_ = t.tty.Close() // the binary holds its own copy
	go func() {
		defer close(t.done)
		buf := make([]byte, 4096)
		for {
			n, err := t.ptmx.Read(buf)
			t.text.Write(buf[:n])
			t.reply()
			if err != nil {
				return // the binary closed the terminal: EOF, or EIO on Linux
			}
		}
	}()
}

// reply types one reply for each prompt in the text that has none yet.
func (t *terminal) reply() {
	if t.script.Expect == "" || len(t.script.Replies) == 0 {
		return
	}
	for strings.Count(t.text.String(), t.script.Expect) > t.answered {
		reply := t.script.Replies[min(t.answered, len(t.script.Replies)-1)]
		t.answered++
		if _, err := io.WriteString(t.ptmx, reply); err != nil && t.err == nil {
			t.err = fmt.Errorf("typing a reply: %w", err)
		}
	}
}

// finish returns everything the binary printed. The caller has waited for
// the binary and killed its group, so the terminal has no writer left.
func (t *terminal) finish() string {
	<-t.done
	_ = t.ptmx.Close()
	if t.err == nil && t.answered == 0 && t.script.Expect != "" {
		t.err = fmt.Errorf("the binary never printed %q", t.script.Expect)
	}
	// A terminal turns "\n" into "\r\n".
	return strings.ReplaceAll(t.text.String(), "\r\n", "\n")
}

// defaultClock are the operations only a wait loop calls.
var defaultClock = []fleettest.Operation{fleettest.EKSDescribeUpdate, fleettest.EKSDescribeInsightsRefresh}

// event is one request of the run, to AWS or to Kubernetes, in the order
// the two servers received them.
type event struct {
	aws  *awsxtest.Request
	kube *kubetest.Request
	// answered and the answer fields are set when the request ends.
	answered   bool
	awsAnswer  awsxtest.Answer
	kubeAnswer kubetest.Answer
}

// operation returns the operation of the event's request.
func (e *event) operation() fleettest.Operation {
	if e.aws != nil {
		return e.aws.Operation
	}
	return e.kube.Operation
}

// recorder moves the world's clock, sends the scenario's signal, makes the
// scenario's moves, and keeps the requests and their answers, as they
// arrive.
type recorder struct {
	sc     Scenario
	world  *fleettest.World
	signal func(os.Signal) error

	mu sync.Mutex
	// paused is set while the scenario's preview runs: its requests move
	// the clock and are not recorded.
	paused bool
	events []*event
	// byAWS and byKube find the event of a request by its ID.
	byAWS, byKube map[int]*event
	// seen counts the requests of each operation, for the moves.
	seen map[fleettest.Operation]int
	// polled holds what the binary has polled since the clock last moved.
	polled    map[string]bool
	mutations int // mutating requests that arrived
	matched   int // requests the scenario's Signal matched
	// signalAfter is the number of mutating requests that had arrived when
	// the signal was sent, or nil when none was sent.
	signalAfter *int
	failure     error
}

func (rec *recorder) onRequest(ctx context.Context, r awsxtest.Request) {
	clock := rec.sc.Clock
	if clock == nil {
		clock = defaultClock
	}
	rec.mu.Lock()
	paused := rec.paused
	if !paused {
		rec.add(&event{aws: &r})
		if r.Mutates {
			rec.mutations++
		}
	}
	if slices.Contains(clock, r.Operation) {
		poll := fmt.Sprint(r.Operation, r.Region, r.Input["name"], r.Input["clusterName"], r.Input["nodegroupName"], r.Input["addonName"], r.Input["updateId"])
		if rec.polled[poll] {
			// A second poll of the same thing: a new round. The clock moves
			// under the lock, so no other poll of the round overtakes it.
			rec.world.Advance(cmp.Or(rec.sc.Step, time.Minute))
			rec.polled = nil
		}
		if rec.polled == nil {
			rec.polled = map[string]bool{}
		}
		rec.polled[poll] = true
	}
	rec.mu.Unlock()
	if paused {
		return
	}
	rec.move(r.Operation)

	s := rec.sc.Signal
	if s == nil || !s.Match(r) {
		return
	}
	rec.mu.Lock()
	rec.matched++
	send := rec.matched == s.Nth
	if send {
		n := rec.mutations
		rec.signalAfter = &n
	}
	rec.mu.Unlock()
	if !send {
		return
	}
	if err := rec.signal(s.Signal); err != nil {
		rec.mu.Lock()
		rec.failure = fmt.Errorf("sending %s: %w", s.Signal, err)
		rec.mu.Unlock()
		return
	}
	// Hold the request until the binary gives it up, so the signal always
	// lands with this request unanswered.
	<-ctx.Done()
}

func (rec *recorder) onAnswer(r awsxtest.Request, a awsxtest.Answer) {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if e := rec.byAWS[r.ID]; e != nil {
		e.answered, e.awsAnswer = true, a
	}
}

func (rec *recorder) onKubeRequest(_ context.Context, r kubetest.Request) {
	rec.mu.Lock()
	paused := rec.paused
	if !paused {
		rec.add(&event{kube: &r})
	}
	rec.mu.Unlock()
	if !paused {
		rec.move(r.Operation)
	}
}

func (rec *recorder) onKubeAnswer(r kubetest.Request, a kubetest.Answer) {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if e := rec.byKube[r.ID]; e != nil {
		e.answered, e.kubeAnswer = true, a
	}
}

// add records an event. The caller holds rec.mu.
func (rec *recorder) add(e *event) {
	if rec.byAWS == nil {
		rec.byAWS, rec.byKube, rec.seen = map[int]*event{}, map[int]*event{}, map[fleettest.Operation]int{}
	}
	rec.events = append(rec.events, e)
	if e.aws != nil {
		rec.byAWS[e.aws.ID] = e
	} else {
		rec.byKube[e.kube.ID] = e
	}
	rec.seen[e.operation()]++
}

// resume ends the preview: from here on the requests are recorded.
func (rec *recorder) resume() {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	rec.paused, rec.polled = false, nil
}

// move makes the scenario's moves that are due at this request of op,
// before the request is answered.
func (rec *recorder) move(op fleettest.Operation) {
	rec.mu.Lock()
	n := rec.seen[op]
	rec.mu.Unlock()
	for _, m := range rec.sc.Moves {
		if m.Before != op || m.Nth != n {
			continue
		}
		if err := m.Do(rec.world); err != nil {
			rec.mu.Lock()
			rec.failure = fmt.Errorf("the move before request %d of %s: %w", n, op, err)
			rec.mu.Unlock()
		}
	}
}

func (rec *recorder) err() error {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if rec.failure != nil {
		return rec.failure
	}
	if s := rec.sc.Signal; s != nil && rec.matched < s.Nth {
		return fmt.Errorf("the signal was never sent: %d matching requests arrived, want %d", rec.matched, s.Nth)
	}
	for _, m := range rec.sc.Moves {
		if rec.seen[m.Before] < m.Nth {
			return fmt.Errorf("a move was never made: %s arrived %d times, want %d", m.Before, rec.seen[m.Before], m.Nth)
		}
	}
	return nil
}
