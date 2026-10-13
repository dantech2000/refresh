//go:build unix

package parity

import (
	"cmp"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/dantech2000/refresh/internal/awsx/awsxtest"
	"github.com/dantech2000/refresh/internal/fleet/fleettest"
)

// Trace is what one run did.
//
// Requests holds every request of the run with two sequence numbers: when
// it arrived and when its answer was ready. What the binary can have known
// when it sent a change is the answers that were complete by then, so the
// trace is built on those numbers and not on arrival order alone. See
// README.md for the rule.
//
// The other fields are derived from Requests. They are what a golden file
// holds and what Compare reads:
//
//   - Changes: each mutating request, in arrival order, with the evidence
//     the binary held when it sent it
//   - Settlement: what the binary saw of the changed resources and their
//     updates after its last change
//   - Verification: what else it saw after the last update ended
//   - Decision: for a run that changed nothing, what it saw
//
// Evidence is the last value the binary saw of each fact. How often and in
// what order it read is not in it: a binary reads in parallel and polls on
// real tickers.
//
// A watch is a stream and not an answer. A run that draws the live node
// view (Scenario.Live) polls the nodes, their pods, the events, and the
// scaling activities on tickers of its own, and what those polls see
// depends on timing. Those requests carry no facts. Operations still lists
// them.
type Trace struct {
	ExitCode int `json:"exitCode"`
	// Stdout and Stderr are what the binary printed, normalised (see
	// normaliser). They are empty for a run on a terminal, whose transcript
	// is in Terminal and is not part of a golden file: it holds spinner
	// frames, which depend on timing.
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	Terminal string `json:"-"`
	// RefusedBeforeMutation is set when the run failed and had sent no
	// mutating request.
	RefusedBeforeMutation bool `json:"refusedBeforeMutation"`

	Changes      []Change   `json:"changes"`
	Settlement   []Evidence `json:"settlement,omitempty"`
	Verification []Evidence `json:"verification,omitempty"`
	Decision     []Evidence `json:"decision,omitempty"`
	// Operations are the distinct operations the binary called.
	Operations []fleettest.Operation `json:"operations"`
	// SignalAfterMutations is the number of mutating requests that had
	// arrived when the scenario's signal was sent. It is nil when the
	// scenario sends none.
	SignalAfterMutations *int `json:"signalAfterMutations,omitempty"`
	// World is the final state of every cluster a request named, by
	// "region/cluster".
	World map[string]ClusterState `json:"world"`

	// Requests are the requests in arrival order, and Confirmations the
	// sequence numbers at which a prompt was answered. They differ between
	// two runs of one binary, so no golden file holds them.
	Requests      []Record `json:"-"`
	Confirmations []int    `json:"-"`
	// PeakConcurrency is the most requests the binary had open at once. It
	// differs between runs of one binary: hold it to a limit.
	PeakConcurrency int `json:"-"`
	// Elapsed is how long the binary ran on the real clock, and
	// WorldElapsed how far the world's clock moved meanwhile.
	Elapsed      time.Duration `json:"-"`
	WorldElapsed time.Duration `json:"-"`
}

// Record is one request of a run.
type Record struct {
	Operation fleettest.Operation
	Region    string
	// Target is the EKS resource the request names: "cluster",
	// "cluster/nodegroup", or "cluster/addon". For a Kubernetes request it
	// is the cluster.
	Target string
	// Observed is the answer in one line: a status, a version, the names
	// in a list, or "error=<code>".
	Observed string
	// Arrived and Completed are sequence numbers of the run. Completed is 0
	// for a request that got no answer.
	Arrived, Completed int
	// Mutation is set for a request that changes AWS state.
	Mutation *Mutation
	// Facts are what the answer said. A mutation, a watch, and a read whose
	// timing is not stable carry none.
	Facts []Fact
}

// Fact is one thing an answer said about the fleet.
type Fact struct {
	// Subject is a kind and a path, such as "nodegroup prod/ng-a",
	// "update prod/ng-a 3", "node prod/a-1", or "pods prod".
	Subject string
	// Attribute is what about it: "status", "version", "condition", "read".
	Attribute string
	Value     string
}

// Change is one mutating request and the evidence the binary held for it.
type Change struct {
	Mutation Mutation `json:"mutation"`
	// Evidence is the last value of each fact the binary saw in the
	// mutation's window: after its previous mutation arrived, and before
	// this one did. See Trace.evidence for what is carried into it.
	Evidence []Evidence `json:"evidence"`
}

// Evidence is the last value a binary saw of one fact in one part of its
// run. In a golden file it is one line: "kind: subject: attribute=value".
type Evidence struct {
	// Kind says what the fact is to the change. For a Change: "target" (the
	// resource it changes, and its cluster), "prerequisite" (an update, or
	// a resource the run changed before), or "gate" (anything else in the
	// cluster). Outside a Change: "settlement", "verification", "decision".
	Kind string
	// BeforePrompt is set for evidence the binary had when it asked the
	// user, in a window in which the user answered a prompt. Evidence
	// without it was read after the answer.
	BeforePrompt bool
	// Carried is set for evidence of a change's target that the binary read
	// before its previous change, and not since.
	Carried bool
	Fact
}

const (
	beforePrompt = "before the prompt, "
	carried      = "carried, "
)

func (e Evidence) String() string {
	prefix := ""
	if e.BeforePrompt {
		prefix = beforePrompt
	}
	if e.Carried {
		prefix = carried
	}
	return fmt.Sprintf("%s%s: %s: %s=%s", prefix, e.Kind, e.Subject, e.Attribute, e.Value)
}

// MarshalText writes the evidence as one line.
func (e Evidence) MarshalText() ([]byte, error) { return []byte(e.String()), nil }

// UnmarshalText reads what MarshalText wrote.
func (e *Evidence) UnmarshalText(text []byte) error {
	rest, before := strings.CutPrefix(string(text), beforePrompt)
	rest, held := strings.CutPrefix(rest, carried)
	kind, rest, ok1 := strings.Cut(rest, ": ")
	subject, rest, ok2 := strings.Cut(rest, ": ")
	attribute, value, ok3 := strings.Cut(rest, "=")
	if !ok1 || !ok2 || !ok3 {
		return fmt.Errorf("evidence %q is not \"kind: subject: attribute=value\"", text)
	}
	*e = Evidence{Kind: kind, BeforePrompt: before, Carried: held, Fact: Fact{Subject: subject, Attribute: attribute, Value: value}}
	return nil
}

// Mutation is one request that changes AWS state.
type Mutation struct {
	Operation fleettest.Operation `json:"operation"`
	Region    string              `json:"region"`
	Target    string              `json:"target"`
	// Payload is the request's input without the fields that name the
	// target. A client request token is renamed token-1, token-2, and so
	// on by first use in the run, so two requests with the same token
	// still show the same name.
	Payload map[string]any `json:"payload"`
	// Outcome is how the request ended: "accepted", with the update it
	// started; "lost", when the change was made and the answer was dropped;
	// "error=<code>"; or "dropped".
	Outcome string `json:"outcome"`
}

// ClusterState is the part of a cluster a change can touch. Each resource
// is one line: version, status, and then what else can change.
type ClusterState struct {
	Cluster    string            `json:"cluster"`
	Nodegroups map[string]string `json:"nodegroups"`
	Addons     map[string]string `json:"addons"`
	// Nodes and Pods are the Kubernetes side, for a cluster that has one:
	// each node with its nodegroup and condition, each pod with its phase
	// and node.
	Nodes map[string]string `json:"nodes,omitempty"`
	Pods  map[string]string `json:"pods,omitempty"`
	// Updates are the cluster's updates, oldest first.
	Updates []string `json:"updates"`
}

// liveViewReads are the operations the live node view polls on its own
// tickers.
var liveViewReads = []fleettest.Operation{
	fleettest.AutoScalingDescribeScalingActivities,
	fleettest.KubeListNodes, fleettest.KubeListPods, fleettest.KubeListEvents,
}

// Mutations returns the mutations of the trace, in order.
func (t Trace) Mutations() []Mutation {
	out := []Mutation{}
	for _, c := range t.Changes {
		out = append(out, c.Mutation)
	}
	return out
}

// buildTrace turns the recorder's events into records, derives the rest of
// the trace from them, and reads the final state of the world.
func buildTrace(sc Scenario, n *normaliser, rec *recorder, world *fleettest.World) Trace {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	trace := Trace{Confirmations: slices.Clone(rec.confirmations)}
	cluster := cmp.Or(sc.Cluster, "prod")
	touched := map[string]bool{Region + "/" + cluster: true}
	unstable := slices.Clone(sc.UnstableReads)
	if sc.Live {
		unstable = append(unstable, liveViewReads...)
	}
	mutationsBeforeSignal := 0
	for _, e := range rec.events {
		record := Record{Operation: e.operation(), Region: Region, Target: cluster, Arrived: e.arrived, Completed: e.completed}
		if e.kube != nil {
			record.Observed = observeKube(e)
			watch := record.Operation == fleettest.KubeWatchNodes || record.Operation == fleettest.KubeWatchEvents
			if !watch && !slices.Contains(unstable, record.Operation) && e.completed != 0 {
				record.Facts = kubeFacts(cluster, e)
			}
			trace.Requests = append(trace.Requests, record)
			continue
		}
		r, answer := *e.aws, e.awsAnswer
		if e.completed == 0 {
			answer = awsxtest.Answer{Dropped: true} // the binary was gone before the handler got to it
		}
		record.Region = r.Region
		record.Target, record.Observed = observe(r, answer)
		if c, _, _ := strings.Cut(record.Target, "/"); c != "" {
			touched[r.Region+"/"+c] = true
		}
		switch {
		case r.Mutates:
			record.Mutation = n.mutation(r, record.Target, record.Observed)
			if rec.signalAt != 0 && e.arrived < rec.signalAt {
				mutationsBeforeSignal++
			}
		case !slices.Contains(unstable, r.Operation):
			record.Facts = awsFacts(r, answer, record.Target)
		}
		trace.Requests = append(trace.Requests, record)
	}
	if rec.signalAt != 0 {
		trace.SignalAfterMutations = &mutationsBeforeSignal
	}
	trace.derive()
	trace.World = worldState(world, slices.Sorted(maps.Keys(touched)))
	return trace
}

// derive fills Changes, Settlement, Verification, Decision, and Operations
// from Requests and Confirmations.
func (t *Trace) derive() {
	t.Changes, t.Settlement, t.Verification, t.Decision = []Change{}, nil, nil, nil
	mutations := t.mutations()
	ops := map[fleettest.Operation]bool{}
	for _, r := range t.Requests {
		ops[r.Operation] = true
	}
	t.Operations = slices.Sorted(maps.Keys(ops))
	if len(mutations) == 0 {
		for _, f := range t.lastSeen(0, 0, 0) {
			t.Decision = append(t.Decision, Evidence{Kind: "decision", Fact: f})
		}
		return
	}

	changed := map[string]bool{} // the targets of all mutations
	clusters := map[string]bool{}
	for i, m := range mutations {
		t.Changes = append(t.Changes, Change{Mutation: *m.Mutation, Evidence: t.evidence(mutations, i)})
		cluster, _, _ := strings.Cut(m.Target, "/")
		changed[m.Target], clusters[cluster] = true, true
	}

	// After the last mutation: the changed resources and the updates are the
	// settlement, and what the binary read of anything else after it saw the
	// last update end is the verification.
	last := mutations[len(mutations)-1]
	settles := func(f Fact) bool {
		return f.kind() == "update" || f.kind() == "insights" || (resource(f.kind()) && changed[f.path()])
	}
	for _, f := range t.lastSeen(last.Arrived, last.Arrived, 0) {
		if settles(f) {
			t.Settlement = append(t.Settlement, Evidence{Kind: "settlement", Fact: f})
		}
	}
	settled := t.settledAt(last)
	if settled < 0 {
		return
	}
	for _, f := range t.lastSeen(last.Arrived, settled, 0) {
		cluster, _, _ := strings.Cut(f.path(), "/")
		if !settles(f) && clusters[cluster] {
			t.Verification = append(t.Verification, Evidence{Kind: "verification", Fact: f})
		}
	}
}

// mutations returns the mutating requests, in arrival order.
func (t *Trace) mutations() []Record {
	var out []Record
	for _, r := range t.Requests {
		if r.Mutation != nil {
			out = append(out, r)
		}
	}
	return out
}

// evidence returns what the binary held for the mutation at index i: the
// last value of each fact of the mutation's cluster among the answers that
// came after the mutation before arrived, and before this one did.
//
// A binary that decides on several targets and then starts them one after
// another reads its second target before its first change, and not again.
// That is no fault. So what it knew of the target from before the mutation
// before is evidence too, marked as carried, unless the run changed that
// resource since. A prompt the user answered in between ends that: what
// the binary read before the answer is not carried over it.
func (t *Trace) evidence(mutations []Record, i int) []Evidence {
	m := mutations[i]
	cluster, _, _ := strings.Cut(m.Target, "/")
	changed := map[string]bool{}
	for _, earlier := range mutations[:i] {
		changed[earlier.Target] = true
	}
	lower := 0
	if i > 0 {
		lower = mutations[i-1].Arrived
	}
	kind := func(f Fact) string {
		word, path := f.kind(), f.path()
		switch {
		case path != cluster && !strings.HasPrefix(path, cluster+"/"):
			return "" // another cluster
		case word == "update":
			return "prerequisite"
		case resource(word) && changed[path]:
			return "prerequisite"
		case resource(word) && path == m.Target, word == "cluster":
			return "target"
		}
		return "gate"
	}
	out := []Evidence{}
	add := func(facts []Fact, before bool) {
		for _, f := range facts {
			if k := kind(f); k != "" {
				out = append(out, Evidence{Kind: k, BeforePrompt: before, Fact: f})
			}
		}
	}
	// A prompt the user answered in the window splits it: what the binary
	// knew when it asked, and what it read after the answer.
	confirmed := 0
	for _, c := range t.Confirmations {
		if c < m.Arrived {
			confirmed = c
		}
	}
	if confirmed > lower {
		add(t.lastSeen(lower, lower, confirmed), true)
		add(t.lastSeen(confirmed, confirmed, m.Arrived), false)
		return out
	}
	add(t.lastSeen(lower, lower, m.Arrived), false)
	for _, f := range t.lastSeen(confirmed, confirmed, m.Arrived) {
		fresh := slices.ContainsFunc(out, func(e Evidence) bool { return e.Subject == f.Subject && e.Attribute == f.Attribute })
		if kind(f) == "target" && !fresh {
			out = append(out, Evidence{Kind: "target", Carried: true, Fact: f})
		}
	}
	return out
}

// settledAt returns the sequence number at which the binary knew how the
// last mutation ended: when it first saw the update that mutation started in
// a final status, or -1 if it never did. A mutation that started no update
// has ended with its own answer.
func (t *Trace) settledAt(last Record) int {
	id, started := strings.CutPrefix(last.Mutation.Outcome, "accepted update ")
	if !started {
		return max(last.Arrived, last.Completed)
	}
	subject := "update " + last.Target + " " + shortID(id)
	at := -1
	for _, r := range t.Requests {
		if r.Completed <= last.Arrived || (at >= 0 && r.Completed > at) {
			continue
		}
		for _, f := range r.Facts {
			if f.Subject == subject && f.Attribute == "status" && f.Value != string(fleettest.UpdateInProgress) {
				at = r.Completed
			}
		}
	}
	return at
}

// lastSeen returns the last value of each fact among the requests that
// arrived after the sequence number arrivedAfter, got their answer after
// completedAfter, and got it before completedBefore (0 means no limit).
// "Last" is by the order of the answers. The facts are sorted by subject.
// A read that worked leaves no fact of its own ("read=ok"): it only
// replaces an earlier error.
func (t *Trace) lastSeen(arrivedAfter, completedAfter, completedBefore int) []Fact {
	in := slices.Clone(t.Requests)
	slices.SortStableFunc(in, func(a, b Record) int { return cmp.Compare(a.Completed, b.Completed) })
	type key struct{ subject, attribute string }
	seen := map[key]string{}
	for _, r := range in {
		if r.Completed == 0 || r.Arrived <= arrivedAfter || r.Completed <= completedAfter || (completedBefore != 0 && r.Completed >= completedBefore) {
			continue
		}
		for _, f := range r.Facts {
			seen[key{f.Subject, f.Attribute}] = f.Value
		}
	}
	out := []Fact{}
	for k, v := range seen {
		if k.attribute == "read" && v == "ok" {
			continue
		}
		out = append(out, Fact{Subject: k.subject, Attribute: k.attribute, Value: v})
	}
	slices.SortFunc(out, func(a, b Fact) int {
		return cmp.Or(cmp.Compare(a.Subject, b.Subject), cmp.Compare(a.Attribute, b.Attribute))
	})
	return out
}

// kind returns the first word of the fact's subject, and path the second.
func (f Fact) kind() string {
	word, _, _ := strings.Cut(f.Subject, " ")
	return word
}

func (f Fact) path() string {
	_, rest, _ := strings.Cut(f.Subject, " ")
	path, _, _ := strings.Cut(rest, " ")
	return path
}

// resource reports whether a subject of the kind is something a mutation
// targets.
func resource(kind string) bool { return kind == "cluster" || kind == "nodegroup" || kind == "addon" }

// shortID returns the number of an update the world started, and any other
// ID unchanged.
func shortID(id string) string {
	if rest, ok := strings.CutPrefix(id, "00000000-0000-4000-8000-"); ok {
		return strings.TrimLeft(rest, "0")
	}
	return id
}

// kubeNouns name the collections of the Kubernetes lists, and kubeItems
// the items whose state is evidence, with the attribute their word gives.
var (
	kubeNouns = map[fleettest.Operation]string{
		fleettest.KubeListNamespaces: "namespaces", fleettest.KubeListNodes: "nodes", fleettest.KubeListPods: "pods",
		fleettest.KubeListPDBs: "pdbs", fleettest.KubeListDeployments: "deployments", fleettest.KubeListEvents: "events",
		fleettest.KubeListNodeMetrics: "node-metrics",
	}
	kubeItems = map[fleettest.Operation][2]string{
		fleettest.KubeListNodes: {"node", "condition"}, fleettest.KubeListPods: {"pod", "phase"},
		fleettest.KubeListPDBs: {"pdb", "allowed"}, fleettest.KubeListDeployments: {"deployment", "replicas"},
	}
)

// kubeFacts returns what a Kubernetes list said: whether it could be read,
// the state of each node, pod, budget, and deployment in it, and, for a pod
// list over the whole cluster, which pods are Pending.
func kubeFacts(cluster string, e *event) []Fact {
	op, a := e.kube.Operation, e.kubeAnswer
	collection := kubeNouns[op] + " " + cluster
	switch {
	case a.Dropped:
		return nil
	case a.Reason != "":
		return []Fact{{Subject: collection, Attribute: "read", Value: "error=" + a.Reason}}
	}
	facts := []Fact{{Subject: collection, Attribute: "read", Value: "ok"}}
	pending := []string{}
	for _, word := range strings.Fields(a.Observed) {
		name, value, _ := strings.Cut(word, "=")
		if op == fleettest.KubeListPods {
			value, _, _ = strings.Cut(value, "@") // where it runs is not its state
			if value == "Pending" {
				pending = append(pending, name)
			}
		}
		if item, ok := kubeItems[op]; ok {
			value = strings.TrimPrefix(value, "allows")
			facts = append(facts, Fact{Subject: item[0] + " " + cluster + "/" + name, Attribute: item[1], Value: value})
		}
	}
	whole := e.kube.Namespace == "" && e.kube.LabelSelector == "" && (e.kube.FieldSelector == "" || e.kube.FieldSelector == "status.phase=Pending")
	if op == fleettest.KubeListPods && whole {
		slices.Sort(pending)
		facts = append(facts, Fact{Subject: collection, Attribute: "pending", Value: cmp.Or(strings.Join(pending, ","), "none")})
	}
	return facts
}

// awsFacts returns what the answer to an AWS read said about the state of
// a cluster, a nodegroup, an add-on, an update, or an insight. The other
// reads (lists of names, AMI parameters, instances, metrics, quotas) are
// input to a plan and carry no fact.
func awsFacts(r awsxtest.Request, a awsxtest.Answer, target string) []Fact {
	subject := ""
	switch r.Operation {
	case fleettest.EKSDescribeCluster:
		subject = "cluster " + target
	case fleettest.EKSDescribeNodegroup:
		subject = "nodegroup " + target
	case fleettest.EKSDescribeAddon:
		subject = "addon " + target
	case fleettest.EKSDescribeUpdate:
		subject = "update " + target + " " + shortID(fmt.Sprint(r.Input["updateId"]))
	case fleettest.EKSListInsights, fleettest.EKSDescribeInsightsRefresh:
		subject = "insights " + target
	case fleettest.EKSDescribeInsight:
		subject = "insight " + target + "/" + fmt.Sprint(r.Input["id"])
	default:
		return nil
	}
	var body map[string]any
	switch {
	case a.Code != "":
		return []Fact{{Subject: subject, Attribute: "read", Value: "error=" + a.Code}}
	case a.Dropped || a.JSON == nil || json.Unmarshal(a.JSON, &body) != nil:
		return nil
	}
	obj := func(m map[string]any, key string) map[string]any {
		v, _ := m[key].(map[string]any)
		return v
	}
	facts := []Fact{{Subject: subject, Attribute: "read", Value: "ok"}}
	add := func(subject, attribute string, value any) {
		facts = append(facts, Fact{Subject: subject, Attribute: attribute, Value: fmt.Sprint(value)})
	}
	issues := func(m map[string]any) string {
		return cmp.Or(strings.TrimPrefix(issuesIn(m), " issues="), "none")
	}
	switch r.Operation {
	case fleettest.EKSDescribeCluster:
		c := obj(body, "cluster")
		add(subject, "version", c["version"])
		add(subject, "status", c["status"])
		add(subject, "issues", issues(c))
	case fleettest.EKSDescribeNodegroup:
		ng := obj(body, "nodegroup")
		s := obj(ng, "scalingConfig")
		add(subject, "version", ng["version"])
		add(subject, "release", ng["releaseVersion"])
		add(subject, "status", ng["status"])
		add(subject, "size", fmt.Sprintf("%v/%v/%v", s["desiredSize"], s["minSize"], s["maxSize"]))
		add(subject, "issues", issues(ng))
	case fleettest.EKSDescribeAddon:
		ad := obj(body, "addon")
		add(subject, "version", ad["addonVersion"])
		add(subject, "status", ad["status"])
		add(subject, "issues", issues(ad))
	case fleettest.EKSDescribeUpdate:
		add(subject, "status", obj(body, "update")["status"])
	case fleettest.EKSDescribeInsightsRefresh:
		add(subject, "refresh", body["status"])
	case fleettest.EKSListInsights:
		list, _ := body["insights"].([]any)
		for _, i := range list {
			if in, ok := i.(map[string]any); ok {
				add(fmt.Sprintf("insight %s/%v", target, in["id"]), "status", obj(in, "insightStatus")["status"])
			}
		}
	case fleettest.EKSDescribeInsight:
		add(subject, "status", obj(obj(body, "insight"), "insightStatus")["status"])
	}
	return facts
}

// issuesIn renders the codes of the health issues of a resource in an
// answer, or nothing for none.
func issuesIn(m map[string]any) string {
	var codes []string
	health, _ := m["health"].(map[string]any)
	list, _ := health["issues"].([]any)
	for _, i := range list {
		if issue, ok := i.(map[string]any); ok {
			codes = append(codes, fmt.Sprint(issue["code"]))
		}
	}
	if len(codes) == 0 {
		return ""
	}
	return " issues=" + strings.Join(codes, ",")
}

// observeKube returns what a Kubernetes list returned: the selectors of
// the request, and one word per item.
func observeKube(e *event) string {
	var b strings.Builder
	for _, part := range []string{e.kube.Namespace, e.kube.LabelSelector, e.kube.FieldSelector} {
		if part != "" {
			b.WriteString("[" + part + "] ")
		}
	}
	switch a := e.kubeAnswer; {
	case e.completed == 0 || a.Dropped:
		b.WriteString("dropped")
	case a.Reason != "":
		b.WriteString("error=" + a.Reason)
	default:
		b.WriteString(a.Observed)
	}
	return strings.TrimSpace(b.String())
}

// observe returns the EKS resource a request names and the part of its
// answer that a decision can rest on.
func observe(r awsxtest.Request, a awsxtest.Answer) (target, observed string) {
	if r.Service == "eks" {
		in := r.Input
		cluster, _ := in["clusterName"].(string)
		if cluster == "" {
			cluster, _ = in["name"].(string)
		}
		target = cluster
		for _, key := range []string{"nodegroupName", "addonName"} {
			if v, ok := in[key].(string); ok && cluster != "" {
				target += "/" + v
			}
		}
	}
	switch {
	case a.Code != "":
		return target, "error=" + a.Code
	case a.Dropped && a.Committed:
		return target, "lost"
	case a.Dropped:
		return target, "dropped"
	}
	var body map[string]any
	if a.JSON == nil || json.Unmarshal(a.JSON, &body) != nil {
		return target, ""
	}
	return target, observedIn(r, body)
}

// observedIn reads the decisive part of a successful JSON answer.
func observedIn(r awsxtest.Request, body map[string]any) string {
	obj := func(m map[string]any, key string) map[string]any {
		v, _ := m[key].(map[string]any)
		return v
	}
	issues := issuesIn
	names := func(key string) string { return fmt.Sprint(body[key]) }

	switch r.Operation {
	case fleettest.EKSDescribeCluster:
		c := obj(body, "cluster")
		return fmt.Sprintf("%v %v%s", c["version"], c["status"], issues(c))
	case fleettest.EKSDescribeNodegroup:
		ng := obj(body, "nodegroup")
		s := obj(ng, "scalingConfig")
		return nodegroupLine(ng["version"], ng["releaseVersion"], ng["status"], s["desiredSize"], s["minSize"], s["maxSize"], issues(ng))
	case fleettest.EKSDescribeAddon:
		a := obj(body, "addon")
		return fmt.Sprintf("%v %v%s", a["addonVersion"], a["status"], issues(a))
	case fleettest.EKSDescribeUpdate:
		u := obj(body, "update")
		return fmt.Sprintf("update %v %v %v", u["id"], u["type"], u["status"])
	case fleettest.EKSUpdateClusterVersion, fleettest.EKSUpdateNodegroupVersion, fleettest.EKSUpdateNodegroupConfig, fleettest.EKSUpdateAddon:
		return fmt.Sprintf("update %v", obj(body, "update")["id"])
	case fleettest.EKSDescribeInsightsRefresh, fleettest.EKSStartInsightsRefresh:
		return fmt.Sprint(body["status"])
	case fleettest.EKSListInsights:
		var out []string
		list, _ := body["insights"].([]any)
		for _, i := range list {
			if in, ok := i.(map[string]any); ok {
				out = append(out, fmt.Sprintf("%v=%v", in["id"], obj(in, "insightStatus")["status"]))
			}
		}
		return fmt.Sprint(out)
	case fleettest.EKSDescribeInsight:
		in := obj(body, "insight")
		return fmt.Sprintf("%v=%v", in["id"], obj(in, "insightStatus")["status"])
	case fleettest.EKSListClusters:
		return names("clusters")
	case fleettest.EKSListNodegroups:
		return names("nodegroups")
	case fleettest.EKSListAddons:
		return names("addons")
	case fleettest.EKSListUpdates:
		return names("updateIds")
	case fleettest.EKSDescribeClusterVersions:
		var out []string
		list, _ := body["clusterVersions"].([]any)
		for _, v := range list {
			if cv, ok := v.(map[string]any); ok {
				out = append(out, fmt.Sprintf("%v=%v", cv["clusterVersion"], cv["versionStatus"]))
			}
		}
		return fmt.Sprint(out)
	case fleettest.EKSDescribeAddonVersions:
		var out []string
		list, _ := body["addons"].([]any)
		for _, a := range list {
			addon, _ := a.(map[string]any)
			versions, _ := addon["addonVersions"].([]any)
			for _, v := range versions {
				if av, ok := v.(map[string]any); ok {
					out = append(out, fmt.Sprintf("%v@%v", addon["addonName"], av["addonVersion"]))
				}
			}
		}
		return fmt.Sprintf("for %v: %v", r.Input["kubernetesVersion"], out)
	case fleettest.SSMGetParameter:
		return fmt.Sprintf("%v=%v", r.Input["Name"], obj(body, "Parameter")["Value"])
	case fleettest.ServiceQuotasGetServiceQuota:
		return fmt.Sprintf("%v=%v", r.Input["QuotaCode"], obj(body, "Quota")["Value"])
	}
	return ""
}

// worldState reads the final state of the clusters with the given
// "region/cluster" keys. A cluster that is not in the world is left out.
func worldState(w *fleettest.World, keys []string) map[string]ClusterState {
	out := map[string]ClusterState{}
	w.View(func(s *fleettest.State) {
		for _, key := range keys {
			region, name, _ := strings.Cut(key, "/")
			if c := s.Cluster(region, name); c != nil {
				out[key] = stateOf(c)
			}
		}
	})
	return out
}

// nodegroupLine is how a nodegroup reads in a trace, as an observation and
// in the final state: version, release, status, the desired, minimum, and
// maximum size, and the health issues.
func nodegroupLine(version, release, status, desired, lowest, highest any, issues string) string {
	return fmt.Sprintf("%v %v %v %v/%v/%v%s", version, release, status, desired, lowest, highest, issues)
}

// issueCodes renders the codes of health issues, or nothing for none.
func issueCodes(list []fleettest.Issue) string {
	if len(list) == 0 {
		return ""
	}
	var codes []string
	for _, i := range list {
		codes = append(codes, i.Code)
	}
	return " issues=" + strings.Join(codes, ",")
}

// stateOf describes one cluster.
func stateOf(c *fleettest.Cluster) ClusterState {
	state := ClusterState{
		Cluster:    fmt.Sprintf("%s %s%s", c.Version, c.Status, issueCodes(c.HealthIssues)),
		Nodegroups: map[string]string{}, Addons: map[string]string{}, Updates: []string{},
	}
	for _, ng := range c.Nodegroups {
		state.Nodegroups[ng.Name] = nodegroupLine(ng.Version, ng.ReleaseVersion, ng.Status, ng.Desired, ng.Min, ng.Max, issueCodes(ng.HealthIssues))
	}
	for _, n := range c.Nodes {
		if state.Nodes == nil {
			state.Nodes = map[string]string{}
		}
		line := n.Nodegroup + " Ready"
		if n.NotReady {
			line = n.Nodegroup + " NotReady"
		}
		if n.Unschedulable {
			line += " cordoned"
		}
		state.Nodes[n.Name] = line
	}
	for _, p := range c.Pods {
		if state.Pods == nil {
			state.Pods = map[string]string{}
		}
		state.Pods[p.Namespace+"/"+p.Name] = strings.TrimSuffix(p.Phase+"@"+p.Node, "@")
	}
	for _, a := range c.Addons {
		line := fmt.Sprintf("%s %s%s", a.Version, a.Status, issueCodes(a.HealthIssues))
		if a.ConfigurationValues != "" {
			line += " config=" + a.ConfigurationValues
		}
		state.Addons[a.Name] = line
	}
	for _, u := range c.Updates {
		var params []string
		for _, p := range u.Params {
			params = append(params, p.Type+"="+p.Value)
		}
		line := fmt.Sprintf("%s %s %s %s", cmp.Or(u.Nodegroup+u.Addon, "cluster"), u.Type, u.Status, strings.Join(params, ","))
		if u.Force {
			line += " force"
		}
		if u.RollbackTimeoutMinutes != 0 {
			line += fmt.Sprintf(" rollbackTimeout=%dm", u.RollbackTimeoutMinutes)
		}
		state.Updates = append(state.Updates, line)
	}
	return state
}
