package fleettest

import (
	"fmt"
	"slices"
)

// Reads holds what the next reads of some resources report in place of
// their real state. EKS reads are eventually consistent: a describe can
// still say ACTIVE just after an update started, and a list can leave out
// an update that exists. A test fills it with World.ScriptStatus and
// World.HideUpdates, and a server reads it in World.ViewReads.
type Reads struct {
	// statuses are the statuses the next describes of a resource report,
	// by "region/cluster/name".
	statuses map[string][]ResourceStatus
	// hidden counts, by "region/cluster", the next ListUpdates answers
	// that leave out the updates in progress.
	hidden map[string]int
}

// ScriptStatus makes the next describes of a resource report these
// statuses, one each, in place of its real status. After them the real
// status shows again. name is a nodegroup or an add-on of the cluster, or
// "" for the cluster itself. Nothing else about the resource changes, and
// EKS still acts on the real status.
//
// It is for what a client sees of an eventually consistent API: ACTIVE for
// a moment after an update started, UPDATING for a moment after one ended,
// or a status that recovers on the next read.
//
// The cluster must answer in region when the call is made; it panics
// otherwise, because a script for no cluster would never be read.
func (w *World) ScriptStatus(region, cluster, name string, statuses ...ResourceStatus) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.reads.statuses == nil {
		w.reads.statuses = map[string][]ResourceStatus{}
	}
	key := w.scriptKey("ScriptStatus", region, cluster) + "/" + name
	w.reads.statuses[key] = append(w.reads.statuses[key], statuses...)
}

// HideUpdates makes the next n ListUpdates answers for the cluster and its
// resources leave out the updates that are in progress. The cluster must
// answer in region, as for ScriptStatus.
func (w *World) HideUpdates(region, cluster string, n int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.reads.hidden == nil {
		w.reads.hidden = map[string]int{}
	}
	w.reads.hidden[w.scriptKey("HideUpdates", region, cluster)] += n
}

// scriptKey returns the key of a cluster's scripted reads. It is built from
// the cluster's own Region field, as the read side builds it, so a cluster
// with no Region (one that answers in every region) is found too. The
// caller holds w.mu.
func (w *World) scriptKey(method, region, cluster string) string {
	c := w.state.Cluster(region, cluster)
	if c == nil {
		panic(fmt.Sprintf("fleettest: %s: no cluster %q answers in region %q", method, cluster, region))
	}
	return c.Region + "/" + c.Name
}

// Unconsumed returns what a test set up and the world never used: scripted
// statuses that no describe read, ListUpdates answers that HideUpdates was
// to hide, and lost responses of a Fault that no request reached. A test
// calls it at the end to find a script that tested nothing. The lines are
// sorted. Throttles are not reported: a test that models a service that
// stays throttled sets a count far above what any run uses.
func (w *World) Unconsumed() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	var out []string
	for key, left := range w.reads.statuses {
		if len(left) > 0 {
			out = append(out, fmt.Sprintf("ScriptStatus %s: %d statuses not read", key, len(left)))
		}
	}
	for key, left := range w.reads.hidden {
		if left > 0 {
			out = append(out, fmt.Sprintf("HideUpdates %s: %d ListUpdates answers not hidden", key, left))
		}
	}
	for _, r := range w.faults {
		if left := r.LoseResponse - r.lost; left > 0 {
			out = append(out, fmt.Sprintf("Fault %s: %d lost responses not used", r.scope, left))
		}
	}
	slices.Sort(out)
	return out
}

// ViewReads is View for a server that answers a describe or a ListUpdates:
// f also gets the scripted reads, which it uses up.
func (w *World) ViewReads(f func(s *State, r *Reads)) {
	w.mu.Lock()
	defer w.mu.Unlock()
	f(&w.state, &w.reads)
}

// Status returns the status that a describe of the resource reports now:
// the next scripted status, which it uses up, or the real one. A server
// calls it once for each describe it answers. name is "" for the cluster.
func (r *Reads) Status(c *Cluster, name string, status ResourceStatus) ResourceStatus {
	key := c.Region + "/" + c.Name + "/" + name
	next := r.statuses[key]
	if len(next) == 0 {
		return status
	}
	r.statuses[key] = next[1:]
	return next[0]
}

// HidesUpdates starts a ListUpdates answer for the cluster and reports
// whether it leaves out the updates in progress. It uses up one answer of
// HideUpdates. A server calls it once for each answer.
func (r *Reads) HidesUpdates(c *Cluster) bool {
	key := c.Region + "/" + c.Name
	if r.hidden[key] == 0 {
		return false
	}
	r.hidden[key]--
	return true
}
