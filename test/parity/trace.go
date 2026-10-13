//go:build unix

package parity

import (
	"cmp"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/dantech2000/refresh/internal/awsx/awsxtest"
	"github.com/dantech2000/refresh/internal/fleet/fleettest"
)

// Trace is what one run did.
//
// The requests are recorded as Steps. A step is one mutating request and
// the reads that arrived since the mutating request before it. The steps
// are in arrival order, which is stable: a binary makes its changes one
// after another. The reads inside a step are an unordered set, sorted for
// the file: each distinct answer once, with no order and no count. A binary
// sends many reads in parallel and polls on real tickers, so their arrival
// order and their number differ from run to run. The trace answers "had the
// binary seen this before it made that change?", and not "did it read this
// before that?" or "how often did it poll?".
//
// A run on a terminal records no reads at all: the terminal view polls the
// nodes and the scaling activities on tickers of its own, and what those
// polls see depends on timing. Its steps hold the mutations only.
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
	// Steps are the requests. The last step has no mutation when reads
	// followed the last one.
	Steps []Step `json:"steps"`
	// SignalAfterMutations is the number of mutating requests that had
	// arrived when the scenario's signal was sent. It is nil when the
	// scenario sends none.
	SignalAfterMutations *int `json:"signalAfterMutations,omitempty"`
	// UnstableReads are the scenario's read operations whose count depends
	// on timing. They were called; they are in no step.
	UnstableReads []fleettest.Operation `json:"unstableReads,omitempty"`
	// World is the final state of every cluster a request named, by
	// "region/cluster".
	World map[string]ClusterState `json:"world"`
	// PeakConcurrency is the most requests the binary had open at once. It
	// differs between runs of one binary, so it is in no golden file: hold
	// it to a limit.
	PeakConcurrency int `json:"-"`
}

// Step is one mutating request and what came before it.
type Step struct {
	// Reads are the distinct answers to the read requests since the
	// previous step's mutation. Empty for a run on a terminal.
	Reads []Read `json:"reads"`
	// Mutation is nil in a last step that has only reads.
	Mutation *Mutation `json:"mutation,omitempty"`
	// Preconditions are what the binary had observed, since its previous
	// mutation, about the things this mutation depends on: the resource it
	// changes, the cluster, and the resource the previous mutation changed
	// (which shows whether the binary saw that change end before it went
	// on). Each is "operation target: observed", without counts.
	Preconditions []string `json:"preconditions,omitempty"`
}

// Read is one answer the binary got to a read: all requests of the
// operation for the resource that were answered the same.
type Read struct {
	Operation fleettest.Operation `json:"operation"`
	Region    string              `json:"region"`
	// Target is the EKS resource the request names: "cluster",
	// "cluster/nodegroup", or "cluster/addon". It is empty for a request
	// that names none.
	Target string `json:"target,omitempty"`
	// Observed is the part of the answer a decision can rest on: a status,
	// a version, the names in a list, or "error=<code>".
	Observed string `json:"observed,omitempty"`
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
	// Updates are the cluster's updates, oldest first.
	Updates []string `json:"updates"`
}

// Mutations returns the mutations of the trace, in order.
func (t Trace) Mutations() []Mutation {
	var out []Mutation
	for _, s := range t.Steps {
		if s.Mutation != nil {
			out = append(out, *s.Mutation)
		}
	}
	return out
}

// buildTrace turns the request log and the answers into steps and reads
// the final state of the world.
func buildTrace(sc Scenario, n *normaliser, requests []awsxtest.Request, rec *recorder, world *fleettest.World) Trace {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	trace := Trace{Steps: []Step{}, SignalAfterMutations: rec.signalAfter}
	touched := map[string]bool{Region + "/" + cmp.Or(sc.Cluster, "prod"): true}
	reads := map[Read]bool{}
	previous := ""
	flush := func(m *Mutation) {
		step := Step{Reads: []Read{}, Mutation: m}
		for r := range reads {
			step.Reads = append(step.Reads, r)
		}
		slices.SortFunc(step.Reads, func(a, b Read) int {
			return cmp.Or(cmp.Compare(a.Operation, b.Operation), cmp.Compare(a.Target, b.Target), cmp.Compare(a.Region, b.Region), cmp.Compare(a.Observed, b.Observed))
		})
		if m != nil {
			step.Preconditions = preconditions(step.Reads, m.Target, previous)
			previous = m.Target
		}
		trace.Steps = append(trace.Steps, step)
		reads = map[Read]bool{}
	}
	for _, r := range requests {
		answer, answered := rec.answers[r.ID]
		if !answered {
			answer = awsxtest.Answer{Dropped: true} // the binary was gone before the handler got to it
		}
		target, observed := observe(r, answer)
		if cluster, _, _ := strings.Cut(target, "/"); cluster != "" {
			touched[r.Region+"/"+cluster] = true
		}
		switch {
		case r.Mutates:
			flush(n.mutation(r, target, observed))
		case slices.Contains(sc.UnstableReads, r.Operation):
			if !slices.Contains(trace.UnstableReads, r.Operation) {
				trace.UnstableReads = append(trace.UnstableReads, r.Operation)
			}
		case sc.Terminal == nil:
			reads[Read{Operation: r.Operation, Region: r.Region, Target: target, Observed: observed}] = true
		}
	}
	if len(reads) > 0 {
		flush(nil)
	}
	slices.Sort(trace.UnstableReads)
	trace.World = worldState(world, slices.Sorted(maps.Keys(touched)))
	return trace
}

// preconditions picks, from the reads of a step, what the binary observed
// about the target of the step's mutation, about its cluster, and about the
// target of the mutation before.
func preconditions(reads []Read, target, previous string) []string {
	cluster, _, _ := strings.Cut(target, "/")
	var out []string
	for _, r := range reads {
		if r.Target == "" || (r.Target != target && r.Target != cluster && r.Target != previous) {
			continue
		}
		line := fmt.Sprintf("%s %s: %s", r.Operation, r.Target, r.Observed)
		if !slices.Contains(out, line) {
			out = append(out, line)
		}
	}
	return out
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
	issues := func(m map[string]any) string {
		var codes []string
		list, _ := obj(m, "health")["issues"].([]any)
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
	names := func(key string) string { return fmt.Sprint(body[key]) }

	switch r.Operation {
	case fleettest.EKSDescribeCluster:
		c := obj(body, "cluster")
		return fmt.Sprintf("%v %v%s", c["version"], c["status"], issues(c))
	case fleettest.EKSDescribeNodegroup:
		ng := obj(body, "nodegroup")
		s := obj(ng, "scalingConfig")
		return fmt.Sprintf("%v %v %v %v/%v/%v%s", ng["version"], ng["releaseVersion"], ng["status"], s["desiredSize"], s["minSize"], s["maxSize"], issues(ng))
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
	issues := func(list []fleettest.Issue) string {
		if len(list) == 0 {
			return ""
		}
		var codes []string
		for _, i := range list {
			codes = append(codes, i.Code)
		}
		return " issues=" + strings.Join(codes, ",")
	}
	w.View(func(s *fleettest.State) {
		for _, key := range keys {
			region, name, _ := strings.Cut(key, "/")
			c := s.Cluster(region, name)
			if c == nil {
				continue
			}
			state := ClusterState{
				Cluster:    fmt.Sprintf("%s %s%s", c.Version, c.Status, issues(c.HealthIssues)),
				Nodegroups: map[string]string{}, Addons: map[string]string{}, Updates: []string{},
			}
			for _, ng := range c.Nodegroups {
				state.Nodegroups[ng.Name] = fmt.Sprintf("%s %s %s %d/%d/%d%s", ng.Version, ng.ReleaseVersion, ng.Status, ng.Desired, ng.Min, ng.Max, issues(ng.HealthIssues))
			}
			for _, a := range c.Addons {
				line := fmt.Sprintf("%s %s%s", a.Version, a.Status, issues(a.HealthIssues))
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
			out[key] = state
		}
	})
	return out
}
