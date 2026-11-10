package backend

import (
	"strconv"
	"testing"

	"github.com/dantech2000/refresh/internal/change"
)

// logOf returns n notice entries from seq on.
func logOf(seq, n int) []change.LogEntry {
	out := make([]change.LogEntry, n)
	for i := range out {
		out[i] = change.LogEntry{Seq: uint64(seq + i), Event: change.Event{Kind: change.Notice, Text: strconv.Itoa(seq + i)}} //nolint:gosec // a test count
	}
	return out
}

// The log of a change stays near the cap of the handle, keeps the newest
// plan entry in front, and never changes what a published state holds.
func TestAppendLog(t *testing.T) {
	t.Run("a long run stays bounded", func(t *testing.T) {
		var log []change.LogEntry
		for i := range 20 * keptLog {
			log = appendLog(log, logOf(i, 1))
			if len(log) > keptLog+keptLog/4 {
				t.Fatalf("after %d entries the log has %d, want at most %d", i+1, len(log), keptLog+keptLog/4)
			}
		}
		if last := log[len(log)-1].Seq; last != 20*keptLog-1 {
			t.Fatalf("last entry = %d, want the newest", last)
		}
	})
	t.Run("the newest plan entry stays in front", func(t *testing.T) {
		log := append([]change.LogEntry{{Seq: 0, Event: change.Event{Kind: change.PlanReady}}}, logOf(1, keptLog+keptLog/4)...)
		log = appendLog(log, logOf(len(log), 1))
		if len(log) != keptLog+1 || log[0].Kind != change.PlanReady || log[1].Kind != change.Notice {
			t.Fatalf("after the cut: %d entries, first %s then %s; want %d, PlanReady, Notice", len(log), log[0].Kind, log[1].Kind, keptLog+1)
		}
	})
	t.Run("a published log does not change", func(t *testing.T) {
		log := make([]change.LogEntry, 0, 2*keptLog)
		log = appendLog(log, logOf(0, keptLog+keptLog/4))
		published := log
		before := published[len(published)-1].Seq
		log = appendLog(log, logOf(len(log), 1))
		log = appendLog(log, logOf(len(log), 1))
		if published[len(published)-1].Seq != before || published[0].Seq != 0 || log[0].Seq == 0 {
			t.Fatalf("the published log changed, or the cut kept its head")
		}
	})
}

// BenchmarkAppendLog adds one entry at a time to a log at the cap, as the
// backend does on each event of a long roll.
func BenchmarkAppendLog(b *testing.B) {
	log := logOf(0, keptLog)
	next := logOf(keptLog, 1)
	b.ReportAllocs()
	for b.Loop() {
		log = appendLog(log, next)
	}
}
