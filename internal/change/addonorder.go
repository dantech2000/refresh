package change

import (
	"fmt"
	"slices"

	"github.com/dantech2000/refresh/internal/apidoc"
)

// addonDependencies returns the add-ons that must be updated before an
// add-on.
func addonDependencies(name string) []string {
	switch name {
	// Networking first: everything else needs a healthy VPC CNI.
	case "kube-proxy", "coredns", "aws-guardduty-agent":
		return []string{"vpc-cni"}
	// Storage drivers need stable cluster networking.
	case "aws-ebs-csi-driver", "aws-efs-csi-driver", "aws-fsx-csi-driver":
		return []string{"vpc-cni", "coredns"}
	// Observability needs DNS.
	case "amazon-cloudwatch-observability", "adot":
		return []string{"coredns"}
	}
	return nil
}

// sortByDependency returns the add-on names in a safe update order: an
// add-on comes after the add-ons that it depends on, when they are in the
// list. It is Kahn's algorithm, and it keeps the order of names among
// add-ons that do not depend on one another. An add-on that the table
// does not know has no dependency.
func sortByDependency(names []string) []string {
	present := make(map[string]bool, len(names))
	for _, n := range names {
		present[n] = true
	}
	inDegree := make(map[string]int, len(names))
	dependents := make(map[string][]string, len(names))
	for _, n := range names {
		for _, dep := range addonDependencies(n) {
			// A dependency that is not installed is no edge.
			if present[dep] {
				inDegree[n]++
				dependents[dep] = append(dependents[dep], n)
			}
		}
	}
	queue := make([]string, 0, len(names))
	for _, n := range names {
		if inDegree[n] == 0 {
			queue = append(queue, n)
		}
	}
	ordered := make([]string, 0, len(names))
	done := make(map[string]bool, len(names))
	for len(queue) > 0 {
		n := queue[0]
		queue = queue[1:]
		if done[n] {
			continue
		}
		done[n] = true
		ordered = append(ordered, n)
		for _, dependent := range dependents[n] {
			inDegree[dependent]--
			if inDegree[dependent] == 0 {
				queue = append(queue, dependent)
			}
		}
	}
	// The table has no cycle. If one is ever added, the add-ons of the
	// cycle still come out, after the rest.
	for _, n := range names {
		if !done[n] {
			done[n] = true
			ordered = append(ordered, n)
		}
	}
	return ordered
}

// dependentPair returns an add-on of steps that has work and depends on
// another add-on of steps that has work, and that dependency, or "" for
// both. --no-wait cannot keep such a pair in order: it does not wait for
// the dependency before it sends the request of the dependent.
func dependentPair(steps []Step) (dependent, dependency string) {
	for _, s := range steps {
		if !s.work() {
			continue
		}
		for _, dep := range addonDependencies(s.Ref.Name) {
			if slices.ContainsFunc(steps, func(d Step) bool { return d.Ref.Name == dep && d.work() }) {
				return s.Ref.Name, dep
			}
		}
	}
	return "", ""
}

// unfinishedDependency returns the result of the step of a dependency of
// the add-on of s that did not end Succeeded or Skipped, among the steps
// that ran before it. It returns false when s is no add-on step of --all
// that has work, or when every dependency in done finished.
//
// --all updates the add-ons in dependency order, and the order means that
// an add-on is not updated after its dependency failed, timed out, is
// busy, or was not attempted.
func (r *runner) unfinishedDependency(s Step, done []StepResult) (StepResult, bool) {
	if req, ok := r.req.(AddonUpdateRequest); !ok || !req.All || s.Ref.Kind != StepAddon || !s.work() {
		return StepResult{}, false
	}
	for _, dep := range addonDependencies(s.Ref.Name) {
		i := slices.IndexFunc(done, func(d StepResult) bool { return d.Ref.Name == dep })
		if i >= 0 && done[i].Status != StepSucceeded && done[i].Status != StepSkipped {
			return done[i], true
		}
	}
	return StepResult{}, false
}

// dependencyNotDone returns the result of step s, whose dependency dep did
// not finish: NotAttempted, with a failure on the step. Nothing is sent
// for it.
func (r *runner) dependencyNotDone(cp *ClusterPlan, s Step, dep StepResult) StepResult {
	res := StepResult{Ref: s.Ref, Status: StepNotAttempted}
	res.Failure = r.stepFailure(cp, res, apidoc.ReasonNotAttempted,
		fmt.Sprintf("%s was not updated: its dependency %s did not finish (%s); nothing was sent", stepName(s.Ref), dep.Ref.Name, dep.Status))
	return res
}
