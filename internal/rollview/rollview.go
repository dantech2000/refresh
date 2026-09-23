// Package rollview renders the live per-node roll panel — nodes
// draining/joining/terminating, pod eviction, node pressure, and a Warning-event
// feed — driven from live Kubernetes state via internal/noderoll. It is shared
// by `nodegroup update` and the cluster-upgrade orchestrator's nodegroup phase
// so both surface the same view; rendering lives here (the view layer), never in
// a service.
package rollview

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"k8s.io/client-go/kubernetes"

	"github.com/dantech2000/refresh/internal/noderoll"
	"github.com/dantech2000/refresh/internal/render"
	"github.com/dantech2000/refresh/internal/ui"
)

// liveRollPoll is how often the live roll panel re-reads cluster state when
// the observer polls with List calls. Kept snappy (vs the EKS --poll-interval)
// so the view feels live, while staying cheap: a node + pod list every few
// seconds.
const liveRollPoll = 3 * time.Second

// liveRollWatchRepaint is the repaint cadence when the observer is
// watch-backed (informers): snapshots read a local cache, so repainting faster
// costs no API calls.
const liveRollWatchRepaint = 1 * time.Second

// liveRollAppendRepaint is the minimum cadence when the panel appends
// snapshots instead of repainting in place (piped stdout, CI logs, NO_COLOR
// with --live). A 40-minute roll then writes at most ~160 frames, and only
// the ones that changed.
const liveRollAppendRepaint = 15 * time.Second

// maxConsecutiveObserverErrors is how many snapshot reads in a row may fail
// before the panel gives up. A single API blip (throttling, a dropped
// connection) keeps the last frame and retries on the next tick instead of
// ending the panel for the rest of the roll.
const maxConsecutiveObserverErrors = 5

// recentEventCount is how many lifecycle events the panel's feed shows.
const recentEventCount = 6

// rollProgressBarWidth is the cell width of the header's replaced/desired bar.
const rollProgressBarWidth = 16

// podEvictionBarWidth is the cell width of a draining node's eviction bar.
const podEvictionBarWidth = 5

// warnMsgMaxWidth caps a Warning event message, in display cells, so a
// verbose message can't blow out the panel width.
const warnMsgMaxWidth = 80

// simulateTick is the frame cadence of `nodegroup update --simulate`: fast
// enough to play the scripted demo roll in a few seconds.
const simulateTick = 160 * time.Millisecond

// rollRepaintInterval picks the panel cadence. In place, it is the caller's
// poll interval capped at liveRollPoll (defaulting to a faster repaint when
// watch-backed). Appending, it is never shorter than liveRollAppendRepaint.
func rollRepaintInterval(pollInterval time.Duration, watching, inPlace bool) time.Duration {
	poll := pollInterval
	if poll <= 0 || poll > liveRollPoll {
		poll = liveRollPoll
		if watching {
			poll = liveRollWatchRepaint
		}
	}
	if !inPlace && poll < liveRollAppendRepaint {
		poll = liveRollAppendRepaint
	}
	return poll
}

// rollMeta is the static context for a live roll panel.
type rollMeta struct {
	Nodegroup      string
	OldAMI, NewAMI string
	Desired        int
	Frame          int // spinner tick, advanced by the driver
}

var rollSpinner = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

func phaseStatus(p noderoll.Phase) render.Status {
	switch p {
	case noderoll.PhaseReady:
		return render.Healthy
	case noderoll.PhaseJoining:
		return render.Progress
	case noderoll.PhaseDraining:
		return render.Warn
	default:
		return render.Unknown
	}
}

func eventToken(kind noderoll.EventKind) (render.Status, string) {
	switch kind {
	case noderoll.EvtJoining:
		return render.Progress, "scaling up, joining"
	case noderoll.EvtOnline:
		return render.Healthy, "came online (Ready)"
	case noderoll.EvtDraining:
		return render.Warn, "cordoned, draining"
	case noderoll.EvtTerminated:
		return render.Neutral, "drained & terminated"
	default:
		return render.Neutral, string(kind)
	}
}

// rollPanelLines builds the live node-roll panel as a slice of lines (pure, so
// it is golden-testable): a header with overall progress, a per-node table
// (AMI + lifecycle state + pod-eviction bar), and a recent-events feed.
func rollPanelLines(th *render.Theme, snap noderoll.Snapshot, events []noderoll.Event, m rollMeta) []string {
	pal := th.Pal

	oldRemaining := 0
	for _, n := range snap.Nodes {
		if !n.OnTarget {
			oldRemaining++
		}
	}
	replaced := m.Desired - oldRemaining
	if replaced < 0 {
		replaced = 0
	}
	active := snap.Joining > 0 || snap.Draining > 0 || replaced < m.Desired

	head := th.Glyph(render.Healthy)
	if active {
		head = spin(th, m.Frame)
	}
	out := []string{
		head + " " + th.Paint(pal.White, "rolling "+m.Nodegroup) +
			th.Paint(pal.Dim, "   ") + th.Paint(pal.Peach, m.OldAMI) +
			th.Paint(pal.Dim, " → ") + th.Paint(pal.Green, m.NewAMI),
		th.Bar(replaced, m.Desired, rollProgressBarWidth, pal.Green) + "  " +
			th.Paint(pal.White, fmt.Sprintf("%d/%d replaced", replaced, m.Desired)) +
			th.Paint(pal.Dim, " · ") + th.Paint(pal.White, fmt.Sprintf("%d new ready", snap.ReadyTarget)),
		"",
	}

	tbl := th.NewTable(
		ui.Column{Title: "", Min: 1},
		ui.Column{Title: "NODE", Min: 14},
		ui.Column{Title: "AMI", Min: 3},
		ui.Column{Title: "STATE", Min: 10},
	)
	for _, n := range snap.Nodes {
		tbl.Row(
			th.Glyph(phaseStatus(n.Phase)),
			th.Paint(pal.White, n.Name),
			amiWord(th, n.OnTarget),
			nodeStateCell(th, n),
		)
	}
	for _, l := range tbl.Render() {
		out = append(out, "  "+l)
	}

	out = append(out, "", th.Paint(pal.Dim, "events"))
	for _, e := range events {
		st, text := eventToken(e.Kind)
		out = append(out, "    "+th.Glyph(st)+" "+th.Paint(pal.White, e.Node)+th.Paint(pal.Dim, "  "+text))
	}
	if snap.WarningsCapped > 0 {
		out = append(out, "    "+th.Paint(pal.Dim, fmt.Sprintf("(showing first %d warning events)", snap.WarningsCapped)))
	}

	// Warning events explain *why* a node is stuck (failed drain/eviction,
	// sandbox failures) — surfaced beneath the lifecycle feed.
	if len(snap.Warnings) > 0 {
		out = append(out, "", th.Paint(pal.Dim, "warnings"))
		for _, w := range snap.Warnings {
			line := "    " + th.Token(render.Warn, w.Reason) + " " + th.Paint(pal.White, w.Node)
			if msg := oneLineWarn(w.Message); msg != "" {
				line += th.Paint(pal.Dim, "  "+msg)
			}
			out = append(out, line)
		}
	}
	return out
}

// oneLineWarn collapses an event message to a single line and clamps it to
// warnMsgMaxWidth display cells. The cut is rune-safe (never splits a UTF-8
// sequence) and ANSI-aware (escape codes take no width and are kept whole).
func oneLineWarn(s string) string {
	return ui.TruncateANSI(strings.Join(strings.Fields(s), " "), warnMsgMaxWidth)
}

func spin(th *render.Theme, frame int) string {
	if !th.Unicode {
		return th.Paint(th.Pal.Teal, "*")
	}
	return th.Paint(th.Pal.Teal, rollSpinner[frame%len(rollSpinner)])
}

func amiWord(th *render.Theme, onTarget bool) string {
	if onTarget {
		return th.Paint(th.Pal.Green, "new")
	}
	return th.Paint(th.Pal.Peach, "old")
}

func nodeStateCell(th *render.Theme, n noderoll.NodeView) string {
	var s string
	switch n.Phase {
	case noderoll.PhaseReady:
		s = th.Paint(th.Pal.Green, "Ready")
	case noderoll.PhaseJoining:
		s = th.Paint(th.Pal.Dim, "joining (NotReady)")
	case noderoll.PhaseDraining:
		s = th.Paint(th.Pal.Yellow, "draining")
		if n.PodsTotal > 0 {
			evicted := n.PodsTotal - n.Pods
			if evicted < 0 {
				evicted = 0
			}
			s += th.Paint(th.Pal.Dim, " · evicting ") + th.Bar(evicted, n.PodsTotal, podEvictionBarWidth, th.Pal.Yellow) +
				th.Paint(th.Pal.Dim, fmt.Sprintf(" %d/%d pods", evicted, n.PodsTotal))
		}
	default:
		s = th.Paint(th.Pal.Dim, "unknown")
	}
	// Advisory: a node can be Ready yet under pressure (undersized replacement).
	if len(n.Pressure) > 0 {
		s += th.Paint(th.Pal.Dim, " · ") + th.Token(render.Warn, strings.Join(n.Pressure, "+"))
	}
	return s
}

// runRoll drives the live panel from obs until done(snapshot) is true or ctx is
// cancelled, repainting in place on a TTY and appending snapshots when piped.
// A failed snapshot read keeps the last good frame with a one-line error under
// it and retries on the next tick; only maxConsecutiveObserverErrors failures
// in a row end the panel.
func runRoll(ctx context.Context, th *render.Theme, w io.Writer, obs noderoll.Observer, m rollMeta, interval time.Duration, done func(noderoll.Snapshot) bool) error {
	tr := noderoll.NewTracker()
	lr := th.NewLiveRegion(w)
	frame := 0
	failures := 0
	var last []string
	return lr.Run(ctx, interval, func() ([]string, bool) {
		snap, err := obs.Snapshot(ctx)
		if err != nil && ctx.Err() != nil && last != nil {
			// Stopped mid-read (the EKS update finished): keep the last good
			// frame instead of painting a spurious "context canceled" error.
			return last, true
		}
		if err != nil {
			failures++
			if ctx.Err() != nil {
				// Stopped before any good frame: nothing to retry for.
				return []string{th.Token(render.Fail, "observer error: "+err.Error())}, true
			}
			if failures >= maxConsecutiveObserverErrors {
				msg := fmt.Sprintf("observer error: %v (giving up after %d failed reads)", err, failures)
				return append(append([]string(nil), last...), th.Token(render.Fail, msg)), true
			}
			msg := fmt.Sprintf("observer error: %s (retrying, %d/%d)",
				oneLineWarn(err.Error()), failures, maxConsecutiveObserverErrors)
			return append(append([]string(nil), last...), th.Token(render.Warn, msg)), false
		}
		failures = 0
		tr.Observe(snap)
		// Animate the spinner only when repainting in place. Appended
		// snapshots keep a fixed glyph, so an unchanged roll yields an
		// identical frame that the live region skips.
		if lr.InPlace() {
			frame++
		}
		m.Frame = frame
		last = rollPanelLines(th, snap, tr.Recent(recentEventCount), m)
		return last, done(snap)
	})
}

// rollComplete reports whether the desired count of nodes is Ready on the
// target AMI, nothing is draining or joining, and no old (baseline) node
// remains. With surge, enough new nodes can be Ready while every old node is
// still serving, so ReadyTarget alone is not enough.
func rollComplete(desired int) func(noderoll.Snapshot) bool {
	return func(s noderoll.Snapshot) bool {
		if s.Total == 0 || s.ReadyTarget < desired || s.Draining != 0 || s.Joining != 0 {
			return false
		}
		for _, n := range s.Nodes {
			if !n.OnTarget {
				return false
			}
		}
		return true
	}
}

// SimulatedRoll renders the live node-roll panel against a scripted observer
// (no AWS / no cluster). Backs `nodegroup update --simulate` for demos and QA.
func SimulatedRoll(ctx context.Context, nodegroup string) error {
	if nodegroup == "" {
		nodegroup = "spot-burst"
	}
	th := render.Default(os.Stdout)
	m := rollMeta{Nodegroup: nodegroup, OldAMI: "ami-0a1b…", NewAMI: "ami-0f9e…", Desired: 3}
	obs := noderoll.NewScriptedObserver(noderoll.DemoTimeline())

	fmt.Println()
	if err := runRoll(ctx, th, os.Stdout, obs, m, simulateTick, rollComplete(m.Desired)); err != nil {
		return err
	}
	fmt.Println()
	fmt.Println(th.Token(render.Healthy, fmt.Sprintf("%s rolled — %d/%d on %s", nodegroup, m.Desired, m.Desired, m.NewAMI)))
	return nil
}

// LiveRollForUpdate renders the live per-node roll panel for a real update by
// observing live Kubernetes state until every roll-start node is replaced
// (rollComplete), ctx is cancelled, or the timeout fires. Callers run it
// concurrently with the EKS DescribeUpdate wait and cancel ctx once the update
// is terminal (see common.RunAlongside), so a failed roll that never converges
// stops the panel promptly. Purely visual and best-effort: it never returns an
// error, so it cannot affect the update or its exit code — EKS DescribeUpdate
// remains authoritative. Old-vs-new is determined by a
// roll-start baseline (no need to know the target AMI ID up front).
func LiveRollForUpdate(ctx context.Context, kube kubernetes.Interface, nodegroup string, timeout, pollInterval time.Duration) {
	if kube == nil {
		return
	}
	obs := noderoll.NewKubeObserver(kube, nodegroup, "")
	// Prefer watch streams (informers) over per-poll List calls: node/pod/event
	// changes surface as they happen, and API load drops to one stream per
	// resource. On failure (e.g. RBAC without watch) the observer just polls.
	watching := obs.StartInformers(ctx) == nil
	defer obs.StopInformers()
	if err := obs.CaptureBaseline(ctx); err != nil {
		return // can't read the cluster — degrade to the standard monitor
	}
	snap0, err := obs.Snapshot(ctx)
	if err != nil || snap0.Total == 0 {
		return
	}
	desired := snap0.Total

	th := render.Default(os.Stdout)
	poll := rollRepaintInterval(pollInterval, watching, th.NewLiveRegion(os.Stdout).InPlace())
	rollCtx := ctx
	if timeout > 0 {
		var cancel context.CancelFunc
		rollCtx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	m := rollMeta{Nodegroup: nodegroup, OldAMI: "current AMI", NewAMI: "recommended AMI", Desired: desired}
	fmt.Println()
	_ = runRoll(rollCtx, th, os.Stdout, obs, m, poll, rollComplete(desired))
}
