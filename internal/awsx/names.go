package awsx

import (
	"fmt"
	"regexp"
	"unicode/utf8"
)

// namePattern is the character rule that EKS gives for a cluster name
// (CreateCluster, name) and the EKS User Guide gives for a managed
// nodegroup name: letters, digits, hyphens, and underscores, starting with
// a letter or a digit.
var namePattern = regexp.MustCompile(`^[0-9A-Za-z][A-Za-z0-9\-_]*$`)

// The longest names that EKS accepts. The EKS API reference documents no
// rule for an add-on name: it must be a name that DescribeAddonVersions
// returns. Those names follow the cluster rule, so refresh checks an
// add-on name against it.
const (
	maxClusterName   = 100
	maxNodegroupName = 63
	maxAddonName     = 100
)

// CheckClusterName returns an error when name cannot be the name of an EKS
// cluster: 1 to 100 characters, letters, digits, hyphens, and underscores,
// starting with a letter or a digit. It makes no call.
func CheckClusterName(name string) error { return checkName("cluster", name, maxClusterName) }

// CheckNodegroupName returns an error when name cannot be the name of a
// managed nodegroup: 1 to 63 characters, with the characters of a cluster
// name.
func CheckNodegroupName(name string) error {
	return checkName("nodegroup", name, maxNodegroupName)
}

// CheckAddonName returns an error when name cannot be the name of an EKS
// add-on: 1 to 100 characters, with the characters of a cluster name.
func CheckAddonName(name string) error { return checkName("add-on", name, maxAddonName) }

// shownName is the most characters of a bad name that an error repeats.
const shownName = 40

// NameError is a name that EKS cannot have.
type NameError struct {
	// Kind is what the name is for: cluster, nodegroup, or add-on.
	Kind string
	Name string
	// Max is the longest name of the kind.
	Max int
	// From says where the name came from, such as "REFRESH_CLUSTER", when
	// it was not typed on the command line. The caller sets it.
	From string
}

func (e *NameError) Error() string {
	shown := fmt.Sprintf("%q", e.Name)
	if n := utf8.RuneCountInString(e.Name); n > shownName {
		shown = fmt.Sprintf("%q... (%d characters)", string([]rune(e.Name)[:shownName]), n)
	}
	if e.From != "" {
		shown += " (from " + e.From + ")"
	}
	return fmt.Sprintf("invalid %s name %s: a name has 1 to %d characters, only letters, digits, hyphens, and underscores, and starts with a letter or digit",
		e.Kind, shown, e.Max)
}

func checkName(kind, name string, maxLen int) error {
	if len(name) <= maxLen && namePattern.MatchString(name) {
		return nil
	}
	return &NameError{Kind: kind, Name: name, Max: maxLen}
}
