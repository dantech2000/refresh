package tui

import (
	"cmp"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/dantech2000/refresh/internal/tui/state"
)

// journal keeps every event the UI has seen this session, so an export
// covers the whole session: each pane's feed keeps only its newest lines.
// The Model holds it by pointer; Update is its only user.
type journal struct {
	seen   map[uint64]bool
	events []state.Event
	// floor is the Seq of the newest event dropped for room: an older
	// event still in a feed is not taken again.
	floor   uint64
	dropped int
	// limit is journalCap; tests lower it.
	limit int
}

// journalCap bounds the journal; the oldest events go first.
const journalCap = 100_000

func newJournal() *journal { return &journal{seen: map[uint64]bool{}, limit: journalCap} }

// add takes the events of st it has not seen. An event without a cluster
// gets the cluster of the roll, upgrade, or readiness run it belongs to.
func (j *journal) add(st state.State) {
	take := func(cluster string, evs []state.Event) {
		for _, e := range evs {
			if e.Seq == 0 || e.Seq <= j.floor || j.seen[e.Seq] {
				continue
			}
			j.seen[e.Seq] = true
			if e.Cluster == "" {
				e.Cluster = cluster
			}
			j.events = append(j.events, e)
		}
	}
	take("", st.Feed)
	take("", st.Log)
	for _, r := range st.Rolls {
		take(r.Cluster, r.Events)
	}
	for _, u := range st.Upgrades {
		take(u.Cluster, u.Events)
	}
	for cluster, r := range st.Readiness {
		take(cluster, r.Log)
	}
	if over := len(j.events) - j.limit; over > 0 {
		j.sort()
		for _, e := range j.events[:over] {
			delete(j.seen, e.Seq)
			j.floor = max(j.floor, e.Seq)
		}
		j.events = slices.Delete(j.events, 0, over)
		j.dropped += over
	}
}

func (j *journal) sort() {
	slices.SortStableFunc(j.events, func(a, b state.Event) int { return cmp.Compare(a.Seq, b.Seq) })
}

// exportKind is what an export holds, named as the log tabs are.
type exportKind int

const (
	exportTimeline exportKind = iota // everything but Kubernetes events and AWS calls
	exportKube
	exportAWS
	exportAll
)

func (k exportKind) String() string {
	return [...]string{"timeline", "kube-events", "aws-api", "all"}[k]
}

func (k exportKind) keeps(e state.Event) bool {
	switch k {
	case exportKube:
		return e.Source == state.SourceKube
	case exportAWS:
		return e.Source == state.SourceAWS
	case exportAll:
		return true
	default:
		return e.Source != state.SourceKube && e.Source != state.SourceAWS
	}
}

// of returns the events of kind, oldest first.
func (j *journal) of(k exportKind) []state.Event {
	j.sort()
	var out []state.Event
	for _, e := range j.events {
		if k.keeps(e) {
			out = append(out, e)
		}
	}
	return out
}

// exportDir is where exports go: $XDG_STATE_HOME/refresh/sessions, else
// ~/.local/state/refresh/sessions.
func exportDir() (string, error) {
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		base = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(base, "refresh", "sessions"), nil
}

// exportMsg is the result of an export.
type exportMsg struct {
	path string
	n    int
	err  error
}

// exportCmd writes evs to a new file in dir, off the UI loop. The file is
// tab-separated, one event per line under a header, readable only by the
// user (it can name accounts and resources).
func exportCmd(dir string, k exportKind, evs []state.Event, st state.State, dropped int, now time.Time) tea.Cmd {
	return func() tea.Msg {
		path, err := writeExport(dir, k, evs, st, dropped, now)
		return exportMsg{path: path, n: len(evs), err: err}
	}
}

func writeExport(dir string, k exportKind, evs []state.Event, st state.State, dropped int, now time.Time) (string, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(dir, fmt.Sprintf("refresh-ui-%s-%s.tsv", now.Format("20060102-150405"), k))
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# refresh ui session log: %s, exported %s\n", k, now.Format(time.RFC3339))
	fmt.Fprintf(&b, "# backend %s, profile %s, context %s, %d event(s)", orDash(st.Backend), orDash(st.Profile), orDash(st.Context), len(evs))
	if dropped > 0 {
		fmt.Fprintf(&b, "; the %d oldest event(s) of the session were not kept", dropped)
	}
	b.WriteString("\nTIME\tCLUSTER\tSOURCE\tLEVEL\tSUBJECT\tTEXT\tDETAIL\n")
	for _, e := range evs {
		fmt.Fprintf(&b, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", e.At.Format(time.RFC3339), cell(e.Cluster), sourceName(e.Source), levelName(e.Level), cell(e.Subject), cell(e.Text), cell(e.Detail))
	}
	if _, err := f.WriteString(b.String()); err != nil {
		_ = f.Close()
		return "", err
	}
	return path, f.Close()
}

// cell is one TSV field: tabs and line breaks become spaces, and an empty
// field is "-".
func cell(s string) string {
	return orDash(strings.NewReplacer("\r\n", " ", "\n", " ", "\r", " ", "\t", " ").Replace(s))
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func sourceName(s state.Source) string {
	switch s {
	case state.SourceRoll:
		return "roll"
	case state.SourceKube:
		return "kube"
	case state.SourceAWS:
		return "aws"
	case state.SourceUpgrade:
		return "upgrade"
	case state.SourceCheck:
		return "check"
	case state.SourceAddon:
		return "addon"
	}
	return "unknown"
}

func levelName(l state.Level) string {
	switch l {
	case state.LevelInfo:
		return "info"
	case state.LevelOK:
		return "ok"
	case state.LevelWarn:
		return "warning"
	case state.LevelError:
		return "error"
	case state.LevelProgress:
		return "progress"
	case state.LevelDone:
		return "done"
	}
	return "unknown"
}

// tildePath writes path under the home directory as ~/...: a notice has
// one line for it.
func tildePath(path string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return path
	}
	if rest, ok := strings.CutPrefix(path, home+string(filepath.Separator)); ok {
		return filepath.Join("~", rest)
	}
	return path
}
