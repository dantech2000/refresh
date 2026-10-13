package fleettest

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
func (w *World) ScriptStatus(region, cluster, name string, statuses ...ResourceStatus) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.reads.statuses == nil {
		w.reads.statuses = map[string][]ResourceStatus{}
	}
	key := region + "/" + cluster + "/" + name
	w.reads.statuses[key] = append(w.reads.statuses[key], statuses...)
}

// HideUpdates makes the next n ListUpdates answers for the cluster and its
// resources leave out the updates that are in progress.
func (w *World) HideUpdates(region, cluster string, n int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.reads.hidden == nil {
		w.reads.hidden = map[string]int{}
	}
	w.reads.hidden[region+"/"+cluster] += n
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
