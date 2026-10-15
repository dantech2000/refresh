//go:build unix

package parity

import (
	"fmt"
	"maps"
	"math"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/dantech2000/refresh/internal/awsx/awsxtest"
)

// normaliser replaces what differs between two runs of the same scenario.
// The list is closed:
//
//   - the server's address, which has a random port: "http://fake"
//   - the run's directory: "$DIR"
//   - client request tokens: "token-N", numbered by first use in a request
//   - a timestamp or a date within 30 days of the run, which comes from the
//     real clock or was seeded relative to it: "<now>" when it is within
//     ten minutes of the run, and otherwise its distance from the run in
//     days, such as "<now-1d>" or "<now+6d>", so that the length of a
//     window stays in the text
//
// Nothing else is touched. Durations stay, the configured ones (a poll
// interval, a timeout) included. Timestamps of the world's clock stay: the
// world starts on 2026-01-01 and its clock is deterministic. Update IDs
// stay: the world numbers them.
type normaliser struct {
	url, dir string
	now      time.Time
	tokens   map[string]string
}

func newNormaliser(url, dir string, now time.Time) *normaliser {
	return &normaliser{url: url, dir: dir, now: now.UTC(), tokens: map[string]string{}}
}

func (n *normaliser) token(t string) string {
	if _, ok := n.tokens[t]; !ok {
		n.tokens[t] = fmt.Sprintf("token-%d", len(n.tokens)+1)
	}
	return n.tokens[t]
}

// targetKeys are the input fields that name the target of a mutation.
var targetKeys = []string{"name", "clusterName", "nodegroupName", "addonName"}

// mutation records a mutating request. outcome is what observe returned
// for its answer.
func (n *normaliser) mutation(r awsxtest.Request, target, outcome string) *Mutation {
	m := &Mutation{Operation: r.Operation, Region: r.Region, Target: target, Payload: maps.Clone(r.Input)}
	for _, key := range targetKeys {
		delete(m.Payload, key)
	}
	if t, ok := m.Payload["clientRequestToken"].(string); ok {
		m.Payload["clientRequestToken"] = n.token(t)
	}
	switch {
	case strings.HasPrefix(outcome, "error="), outcome == "lost", outcome == "dropped":
		m.Outcome = outcome
	default:
		m.Outcome = strings.TrimSpace("accepted " + outcome)
	}
	return m
}

// timestamp matches an RFC 3339 timestamp, or a date alone.
var timestamp = regexp.MustCompile(`\b\d{4}-\d{2}-\d{2}(T\d{2}:\d{2}:\d{2}(\.\d+)?(Z|[+-]\d{2}:\d{2}))?`)

// window is how far from the run a timestamp can be and still count as
// taken from the real clock.
const window = 30 * 24 * time.Hour

func (n *normaliser) text(s string) string {
	s = strings.ReplaceAll(s, n.url, "http://fake")
	s = strings.ReplaceAll(s, n.dir, "$DIR")
	// Longest first, so a token that contains another is replaced whole.
	tokens := slices.SortedFunc(maps.Keys(n.tokens), func(a, b string) int { return len(b) - len(a) })
	for _, t := range tokens {
		s = strings.ReplaceAll(s, t, n.tokens[t])
	}
	return timestamp.ReplaceAllStringFunc(s, n.relative)
}

// relative rewrites a timestamp or a date near the run as its distance from
// the run, and returns any other unchanged.
func (n *normaliser) relative(text string) string {
	t, err := time.Parse(time.RFC3339Nano, text)
	dateOnly := err != nil
	if dateOnly {
		if t, err = time.Parse(time.DateOnly, text); err != nil {
			return text
		}
	}
	distance := t.Sub(n.now)
	if dateOnly {
		// A date has no time of day. Compare it with the run's date.
		distance = t.Sub(n.now.Truncate(24 * time.Hour))
	}
	if distance.Abs() > window {
		return text
	}
	if !dateOnly && distance.Abs() <= 10*time.Minute {
		return "<now>"
	}
	days := int(math.Round(distance.Hours() / 24))
	if days == 0 {
		return "<now>"
	}
	return fmt.Sprintf("<now%+dd>", days)
}
