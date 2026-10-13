package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/dantech2000/refresh/internal/apidoc"
	"github.com/dantech2000/refresh/internal/fleet"
	"github.com/dantech2000/refresh/internal/health"
	"github.com/dantech2000/refresh/internal/tui/state"
	"github.com/dantech2000/refresh/internal/view"
)

// The clock of every fixture.
var now = time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)

// scripted is the backend of the model tests: it records what the model
// asked for and answers from a script. Next is never reached: the harness
// does not run the command that waits for the next state, so a test
// feeds states itself.
type scripted struct {
	nextCalls    int
	refreshed    int
	readiness    []state.Key
	readinessErr error
}

func (s *scripted) Next(context.Context, uint64) (state.State, error) {
	s.nextCalls++
	return state.State{}, errors.New("the harness feeds states itself")
}

func (s *scripted) Refresh() { s.refreshed++ }

func (s *scripted) Readiness(_ context.Context, key state.Key) error {
	s.readiness = append(s.readiness, key)
	return s.readinessErr
}

// harness drives a Model. It runs every command a key returns,
// synchronously, and feeds the result back, the way the Bubble Tea
// runtime would, except the commands after a state: the wait for the
// next state, and the tick.
type harness struct {
	t    *testing.T
	b    *scripted
	m    Model
	quit bool
}

func newHarness(t *testing.T, w, h int, th view.Theme) *harness {
	t.Helper()
	b := &scripted{}
	hs := &harness{t: t, b: b, m: New(t.Context(), b, th)}
	hs.update(tea.WindowSizeMsg{Width: w, Height: h})
	return hs
}

var (
	colorTheme = view.Theme{Depth: view.TrueColor, Unicode: true}
	asciiTheme = view.Theme{}
)

// update delivers msg and runs the commands it produces, except after a
// state.
func (h *harness) update(msg tea.Msg) {
	h.t.Helper()
	queue := []tea.Msg{msg}
	for len(queue) > 0 {
		m := queue[0]
		queue = queue[1:]
		if _, ok := m.(tea.QuitMsg); ok {
			h.quit = true
			continue
		}
		if batch, ok := m.(tea.BatchMsg); ok {
			for _, c := range batch {
				if c != nil {
					queue = append(queue, c())
				}
			}
			continue
		}
		next, cmd := h.m.Update(m)
		h.m = next.(Model)
		switch m.(type) {
		case stateMsg, tickMsg:
			// The wait for the next state and the tick are the test's.
			continue
		}
		if cmd != nil {
			queue = append(queue, cmd())
		}
	}
}

// state feeds a published state.
func (h *harness) state(st state.State) { h.update(stateMsg{st: st}) }

// tick moves the clock of the model.
func (h *harness) tick(at time.Time) { h.update(tickMsg{at: at}) }

// keys presses each key in turn.
func (h *harness) keys(ks ...string) {
	h.t.Helper()
	for _, k := range ks {
		next, cmd := h.m.key(k)
		h.m = next
		if cmd != nil {
			h.update(cmd())
		}
	}
}

// text is the frame without styles.
func (h *harness) text() string {
	var b strings.Builder
	for _, l := range h.m.frame() {
		b.WriteString(l.Plain())
		b.WriteByte('\n')
	}
	return b.String()
}

func (h *harness) contains(want ...string) {
	h.t.Helper()
	txt := h.text()
	for _, s := range want {
		if !strings.Contains(txt, s) {
			h.t.Fatalf("frame lacks %q:\n%s", s, txt)
		}
	}
}

func (h *harness) lacks(bad ...string) {
	h.t.Helper()
	txt := h.text()
	for _, s := range bad {
		if strings.Contains(txt, s) {
			h.t.Fatalf("frame has %q:\n%s", s, txt)
		}
	}
}

// checkFrame asserts that the frame is exactly w by h cells.
func checkFrame(t *testing.T, name string, m Model) {
	t.Helper()
	f := m.frame()
	if len(f) != m.h {
		t.Fatalf("%s: %d rows, want %d", name, len(f), m.h)
	}
	for i, l := range f {
		if got := l.Width(); got != m.w {
			t.Fatalf("%s: row %d is %d cells, want %d: %q", name, i, got, m.w, l.Plain())
		}
	}
	if rows := strings.Count(m.render(), "\n") + 1; rows != m.h {
		t.Fatalf("%s: rendered %d rows, want %d", name, rows, m.h)
	}
}

// v parses a version of a fixture.
func v(s string) fleet.Version {
	ver, err := fleet.ParseVersion(s)
	if err != nil {
		panic(err)
	}
	return ver
}

// cluster returns a row of the fixture: an ACTIVE cluster on version
// with two nodegroups and two add-ons, derived as the backend derives it
// for the common cases.
func cluster(name, region, version string) state.Cluster {
	snap := &fleet.Snapshot{
		Cluster: fleet.Cluster{
			Name: name, Region: region, Status: "ACTIVE", Version: v(version),
			ARN:     "arn:aws:eks:" + region + ":123456789012:cluster/" + name,
			Support: fleet.Support{Tier: apidoc.SupportStandard, StandardEnd: now.AddDate(0, 6, 0), ExtendedEnd: now.AddDate(1, 6, 0)},
		},
		Have: fleet.Have{Cluster: fleet.Collected, Nodegroups: fleet.Collected, Addons: fleet.Collected, Support: fleet.Collected},
		Nodegroups: []fleet.Nodegroup{
			{Name: "api", State: fleet.Collected, Status: "ACTIVE", Version: v(version), AMIType: "AL2023_x86_64_STANDARD", Desired: 3, Min: 1, Max: 5, SizesKnown: true, AMI: fleet.AMI{State: fleet.Collected, Current: "ami-1", Latest: "ami-1"}},
			{Name: "web", State: fleet.Collected, Status: "ACTIVE", Version: v(version), AMIType: "AL2023_x86_64_STANDARD", Desired: 2, Min: 1, Max: 3, SizesKnown: true, AMI: fleet.AMI{State: fleet.Collected, Current: "ami-1", Latest: "ami-1"}},
		},
		Addons: []fleet.Addon{
			{Name: "coredns", State: fleet.Collected, Status: "ACTIVE", Version: "v1.11.3-eksbuild.2", Latest: "v1.11.3-eksbuild.2", LatestState: fleet.Collected},
			{Name: "vpc-cni", State: fleet.Collected, Status: "ACTIVE", Version: "v1.20.0-eksbuild.1", Latest: "v1.20.0-eksbuild.1", LatestState: fleet.Collected},
		},
		FetchedAt: now,
	}
	return state.Cluster{Key: state.Key{Region: region, Name: name}, Snapshot: snap, Status: view.Healthy, Version: v(version), Support: apidoc.SupportStandard}
}

// stale makes a row need attention: one outdated AMI and one add-on
// behind.
func stale(c state.Cluster) state.Cluster {
	c.Snapshot.Nodegroups[0].AMI.Current = "ami-0"
	c.Snapshot.Addons[0].Version = "v1.11.1-eksbuild.1"
	c.Stale = state.Stale{AMIs: 1, Addons: 1}
	c.Status = view.Warn
	return c
}

// changing makes a row show a nodegroup in flight.
func changing(c state.Cluster) state.Cluster {
	c.Snapshot.Nodegroups[1].Status = "UPDATING"
	c.InFlight = []string{"nodegroup web UPDATING"}
	c.Status = view.Progress
	return c
}

// incomplete makes a row partly read.
func incomplete(c state.Cluster) state.Cluster {
	c.Snapshot.Nodegroups[1].State = fleet.Failed
	c.Snapshot.Failures = []apidoc.Failure{{Kind: apidoc.FailureNodegroup, Name: "web", Reason: apidoc.ReasonThrottled, Error: "throttled"}}
	c.Incomplete = true
	c.Stale.Unknown = 1
	c.Status = view.Unknown
	return c
}

// extended makes a row out of standard support.
func extended(c state.Cluster) state.Cluster {
	c.Snapshot.Cluster.Support.Tier = apidoc.SupportExtended
	c.Support = apidoc.SupportExtended
	c.Status = view.Fail
	return c
}

// report is a readiness report with warnings, for a cluster that is
// reviewed before an upgrade.
func report() *health.ReadinessReport {
	return &health.ReadinessReport{
		Readiness: apidoc.ReadinessReview,
		Reasons:   []string{"Insights: 1 insight warns about 1.34 (DEPRECATED_APIS)", "VersionSkew: nodegroup web is 1 minor behind"},
		Checks: []health.Result{
			{Name: "Insights", Outcome: health.Warn, Blocking: true, Message: "1 insight warns about 1.34", Details: []string{"DEPRECATED_APIS: 3 clients use apps/v1beta1"}, Findings: []string{"DEPRECATED_APIS"}},
			{Name: "VersionSkew", Outcome: health.Pass, Blocking: true, Message: "every nodegroup is within 3 minors of the control plane"},
			{Name: "ControlPlane", Outcome: health.Unreadable, Message: "the control-plane metrics could not be read", Failures: []apidoc.Failure{{Kind: apidoc.FailureCluster, Name: "prod", Reason: apidoc.ReasonAccessDenied, Error: "cloudwatch:GetMetricData denied"}}},
		},
		Insights:          []apidoc.Insight{{ID: "a1b2c3d4e5", Name: "Deprecated APIs removed in Kubernetes v1.34", Category: "UPGRADE_READINESS", Status: "WARNING", StatusReason: "3 clients use deprecated APIs", KubernetesVersion: "1.34"}, {ID: "f6g7", Name: "Kubelet version skew", Status: "PASSING"}},
		InsightsEvaluated: true,
	}
}

// fleetState returns a fleet of n clusters over two regions, with one
// stale, one changing, one incomplete, one in extended support, and one
// with a readiness report, when n allows.
func fleetState(n int) state.State {
	st := state.State{Version: 1, Now: now, ReadOnly: true, Profile: "prod-admin", Context: "prod",
		Sweep: state.Sweep{Regions: 2, Answered: []string{"eu-west-1", "us-east-1"}, StartedAt: now.Add(-3 * time.Second), EndedAt: now, Count: 1, Interval: time.Minute}}
	for i := range n {
		region := "us-east-1"
		if i%2 == 1 {
			region = "eu-west-1"
		}
		c := cluster(fmt.Sprintf("c-%03d", i), region, "1.33")
		switch i {
		case 1:
			c = stale(c)
		case 2:
			c = changing(c)
		case 3:
			c = incomplete(c)
		case 4:
			c = extended(c)
		case 5:
			c.Readiness = &state.Readiness{StartedAt: now.Add(-20 * time.Second), EndedAt: now.Add(-2 * time.Second), Report: report()}
		case 6:
			c.Readiness = &state.Readiness{StartedAt: now.Add(-5 * time.Second)}
		}
		st.Clusters = append(st.Clusters, c)
	}
	st.Feed = []state.Line{
		{At: now.Add(-3 * time.Second), Status: view.Neutral, Text: fmt.Sprintf("%d clusters: 2 need attention, 1 changing", n)},
		{At: now.Add(-2 * time.Second), Cluster: state.Key{Region: "us-east-1", Name: "c-004"}, Status: view.Fail, Text: "extended support"},
		{At: now, Cluster: state.Key{Region: "eu-west-1", Name: "c-005"}, Status: view.Warn, Text: "readiness: review"},
	}
	return st
}
