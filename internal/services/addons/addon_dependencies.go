package addons

func addonNames(addons []AddonSummary) []string {
	names := make([]string, len(addons))
	for i, a := range addons {
		names[i] = a.Name
	}
	return names
}

// addonDependencies maps addon names to the addons they depend on.
// An addon that appears as a dependency value must be updated before the key.
var addonDependencies = map[string][]string{
	// Networking foundation — everything else depends on VPC CNI being healthy
	"kube-proxy": {"vpc-cni"},
	"coredns":    {"vpc-cni"},

	// Storage drivers depend on cluster networking being stable
	"aws-ebs-csi-driver": {"vpc-cni", "coredns"},
	"aws-efs-csi-driver": {"vpc-cni", "coredns"},
	"aws-fsx-csi-driver": {"vpc-cni", "coredns"},

	// Observability depends on DNS resolution
	"amazon-cloudwatch-observability": {"coredns"},
	"adot":                            {"coredns"},

	// Guard duty depends on networking
	"aws-guardduty-agent": {"vpc-cni"},
}

// sortByDependency returns addons in a safe update order using Kahn's algorithm.
// Addons absent from the dependency map are treated as having no deps and are
// appended after the ordered set.
func sortByDependency(addons []AddonSummary) []AddonSummary {
	nameToAddon := make(map[string]AddonSummary, len(addons))
	for _, a := range addons {
		nameToAddon[a.Name] = a
	}

	// Build an in-degree map and adjacency list restricted to the addons we have.
	inDegree := make(map[string]int, len(addons))
	dependents := make(map[string][]string, len(addons)) // dep → list of addons that need it

	for _, a := range addons {
		if _, ok := inDegree[a.Name]; !ok {
			inDegree[a.Name] = 0
		}
		for _, dep := range addonDependencies[a.Name] {
			if _, present := nameToAddon[dep]; !present {
				// Dep not installed — skip the edge.
				continue
			}
			inDegree[a.Name]++
			dependents[dep] = append(dependents[dep], a.Name)
		}
	}

	// Seed the queue with zero-in-degree nodes (stable order: by original position).
	queue := make([]string, 0, len(addons))
	for _, a := range addons {
		if inDegree[a.Name] == 0 {
			queue = append(queue, a.Name)
		}
	}

	ordered := make([]AddonSummary, 0, len(addons))
	for len(queue) > 0 {
		name := queue[0]
		queue = queue[1:]
		ordered = append(ordered, nameToAddon[name])
		for _, dependent := range dependents[name] {
			inDegree[dependent]--
			if inDegree[dependent] == 0 {
				queue = append(queue, dependent)
			}
		}
	}

	// If there's a cycle (shouldn't happen with the static map, but be safe),
	// append any remaining addons so we don't silently drop them.
	if len(ordered) < len(addons) {
		seen := make(map[string]bool, len(ordered))
		for _, a := range ordered {
			seen[a.Name] = true
		}
		for _, a := range addons {
			if !seen[a.Name] {
				ordered = append(ordered, a)
			}
		}
	}

	return ordered
}
