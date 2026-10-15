// Package fleet reads EKS clusters into snapshots: one model of a cluster
// for every command, with a record of what was read and what was not.
package fleet

import (
	"cmp"
	"fmt"
	"strconv"
	"strings"
)

// Version is a Kubernetes minor version, such as 1.33. The zero value means
// that the version is not known.
type Version struct {
	Major, Minor int
}

// ParseVersion reads a version such as "1.33", "v1.33", or "1.33.2-eks-1".
// It keeps the major and the minor number and ignores what follows them.
// Both numbers must be plain digits: "1.-5" is an error.
func ParseVersion(s string) (Version, error) {
	rest := strings.TrimPrefix(strings.TrimSpace(s), "v")
	major, rest, ok := strings.Cut(rest, ".")
	if !ok {
		return Version{}, fmt.Errorf("invalid Kubernetes version %q: want major.minor", s)
	}
	minor := rest
	if i := strings.IndexAny(rest, ".-+"); i >= 0 {
		minor = rest[:i]
	}
	maj, err1 := parseNumber(major)
	mnr, err2 := parseNumber(minor)
	if err1 != nil || err2 != nil {
		return Version{}, fmt.Errorf("invalid Kubernetes version %q: want major.minor", s)
	}
	return Version{Major: maj, Minor: mnr}, nil
}

// parseNumber reads a number of plain digits. strconv.Atoi alone would
// accept a sign.
func parseNumber(s string) (int, error) {
	if s == "" || strings.Trim(s, "0123456789") != "" {
		return 0, strconv.ErrSyntax
	}
	return strconv.Atoi(s)
}

// String returns the version as "1.33", or "" for the zero value.
func (v Version) String() string {
	if v.IsZero() {
		return ""
	}
	return strconv.Itoa(v.Major) + "." + strconv.Itoa(v.Minor)
}

// IsZero reports whether the version is not known.
func (v Version) IsZero() bool {
	return v == Version{}
}

// Compare returns -1 when v is older than o, +1 when it is newer, and 0
// when they are the same. The zero value is older than every version.
func (v Version) Compare(o Version) int {
	return cmp.Or(cmp.Compare(v.Major, o.Major), cmp.Compare(v.Minor, o.Minor))
}
