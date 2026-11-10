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
// an add-on with work that comes before it in steps, and that
// dependency, or "" for both. --no-wait cannot keep such a pair in order:
// it does not wait for the dependency before it sends the request of the
// dependent. --all puts every dependency first. Named add-ons keep the
// order of the user, and a dependency named after its dependent is no
// pair: the run has no order to keep for it.
func dependentPair(steps []Step) (dependent, dependency string) {
	for i, s := range steps {
		if !s.work() {
			continue
		}
		for _, dep := range addonDependencies(s.Ref.Name) {
			if slices.ContainsFunc(steps[:i], func(d Step) bool { return d.Ref.Name == dep && d.work() }) {
				return s.Ref.Name, dep
			}
		}
	}
	return "", ""
}

// namedAfter returns a notice for each add-on with work whose dependency
// with work comes after it in steps. Only named add-ons can be in that
// order: the run keeps it, so the dependency does not stop its dependent.
func namedAfter(steps []Step) []string {
	var out []string
	for i, s := range steps {
		if !s.work() {
			continue
		}
		for _, dep := range addonDependencies(s.Ref.Name) {
			if slices.ContainsFunc(steps[i+1:], func(d Step) bool { return d.Ref.Name == dep && d.work() }) {
				out = append(out, fmt.Sprintf("add-on %s depends on %s, which is named after it; the order given is kept", s.Ref.Name, dep))
			}
		}
	}
	return out
}

// unfinishedDependency returns the result of the step of a dependency of
// the add-on of s that did not end clean, among the steps that ran before
// it in the run. Clean is verified with no issue: cleanStep, and not in
// unverified, the steps whose check after the change could not read. It
// returns false when s is no add-on step that has work, or when every
// dependency in done ended clean.
//
// An add-on is not updated after its dependency failed, timed out, was
// busy, was not attempted, was updated and is not healthy, or was updated
// and could not be read after it. --all
// orders the add-ons by dependency. Named add-ons run in the order given:
// a dependency named after its dependent has not run when the dependent
// does, so it does not stop it.
func (r *runner) unfinishedDependency(s Step, done []StepResult, unverified map[StepRef]bool) (StepResult, bool) {
	if _, ok := r.req.(AddonUpdateRequest); !ok || s.Ref.Kind != StepAddon || !s.work() {
		return StepResult{}, false
	}
	for _, dep := range addonDependencies(s.Ref.Name) {
		i := slices.IndexFunc(done, func(d StepResult) bool { return d.Ref.Name == dep })
		if i >= 0 && (!cleanStep(done[i]) || unverified[done[i].Ref]) {
			return done[i], true
		}
	}
	return StepResult{}, false
}

// dependencyNotDone returns the result of step s, whose dependency dep did
// not end clean: NotAttempted, with a failure on the step that says why.
// unread says that the check after the update of dep could not read.
// Nothing is sent for it.
func (r *runner) dependencyNotDone(cp *ClusterPlan, s Step, dep StepResult, unread bool) StepResult {
	res := StepResult{Ref: s.Ref, Status: StepNotAttempted}
	why := fmt.Sprintf("its dependency %s did not finish (%s)", dep.Ref.Name, dep.Status)
	switch {
	case dep.Status == StepSucceeded && dep.VerifyIssue != "":
		why = fmt.Sprintf("its dependency %s was updated and is not healthy (%s)", dep.Ref.Name, dep.VerifyIssue)
	case dep.Status == StepSucceeded && unread:
		why = fmt.Sprintf("its dependency %s was updated and could not be read after it, so its health is not known", dep.Ref.Name)
	}
	res.Failure = r.stepFailure(cp, res, apidoc.ReasonNotAttempted, stepName(s.Ref)+" was not updated: "+why+"; nothing was sent")
	return res
}
