package change

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
