package fleet

import (
	"slices"
	"strings"
)

// CompareAddonVersions compares EKS add-on version strings such as
// "v1.18.1-eksbuild.3", returning >0 when a is newer than b. A version is
// core[-prerelease][-eksbuild.N][+meta]:
//
//   - core segments ("1.18.1") compare numerically; a word ranks below a
//     number.
//   - a prerelease ("-rc1", "-alpha.2") ranks below the same core without
//     one, and prereleases compare by semver precedence.
//   - "-eksbuild.N" is the EKS build number, not a prerelease: it ranks
//     above the bare core and compares like the core.
//   - "+meta" breaks any remaining tie, so the order stays total.
func CompareAddonVersions(a, b string) int {
	va, vb := parseAddonVersion(a), parseAddonVersion(b)
	if c := compareSegments(va.core, vb.core); c != 0 {
		return c
	}
	if c := comparePrerelease(va.pre, vb.pre); c != 0 {
		return c
	}
	if c := compareSegments(va.build, vb.build); c != 0 {
		return c
	}
	return compareSegments(va.meta, vb.meta)
}

// ValidAddonVersion reports whether v is an add-on version that
// CompareAddonVersions can order by meaning: a core of one or more
// numeric segments ("1.18.1"), with an optional leading "v" and the
// optional parts above. An empty string and a word such as "latest" are
// not versions; CompareAddonVersions still orders them, but the order
// says nothing about which one is newer.
func ValidAddonVersion(v string) bool {
	core := parseAddonVersion(v).core
	return len(core) > 0 && !slices.ContainsFunc(core, func(s string) bool { return !isDigits(s) })
}

// addonVersion is a version string split into the parts CompareAddonVersions
// orders by, each a list of identifiers.
type addonVersion struct {
	core, pre, build, meta []string
}

// parseAddonVersion splits v for CompareAddonVersions. Of the identifiers
// after the first '-', the ones before an identifier that starts with
// "eksbuild" are the prerelease, and the rest are the build.
func parseAddonVersion(v string) addonVersion {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	idents := func(s string) []string {
		return strings.FieldsFunc(s, func(r rune) bool { return r == '.' || r == '-' })
	}
	main, meta, _ := strings.Cut(v, "+")
	core, suffix, _ := strings.Cut(main, "-")
	out := addonVersion{
		core: strings.FieldsFunc(core, func(r rune) bool { return r == '.' }),
		pre:  idents(suffix),
		meta: idents(meta),
	}
	for i, id := range out.pre {
		if strings.HasPrefix(id, "eksbuild") {
			out.pre, out.build = out.pre[:i], out.pre[i:]
			break
		}
	}
	return out
}

// comparePrerelease orders prereleases by semver precedence: no prerelease
// ranks highest; numeric identifiers compare numerically and rank below
// words; when one list is a prefix of the other, the longer one is newer.
func comparePrerelease(as, bs []string) int {
	switch {
	case len(as) == 0 && len(bs) == 0:
		return 0
	case len(as) == 0:
		return 1
	case len(bs) == 0:
		return -1
	}
	for i := 0; i < len(as) && i < len(bs); i++ {
		an, bn := isDigits(as[i]), isDigits(bs[i])
		switch {
		case an && bn:
			if c := compareDigits(as[i], bs[i]); c != 0 {
				return c
			}
		case an:
			return -1
		case bn:
			return 1
		default:
			if c := strings.Compare(as[i], bs[i]); c != 0 {
				return c
			}
		}
	}
	return len(as) - len(bs)
}

// compareSegments compares two identifier lists segment by segment: numbers
// numerically, words lexically, and a number above a word. When one list is
// a prefix of the other, the longer one is newer.
func compareSegments(as, bs []string) int {
	for i := 0; i < len(as) && i < len(bs); i++ {
		an, bn := isDigits(as[i]), isDigits(bs[i])
		switch {
		case an && bn:
			if c := compareDigits(as[i], bs[i]); c != 0 {
				return c
			}
		case an:
			return 1 // numeric beats non-numeric ("1" > "eksbuild")
		case bn:
			return -1
		default:
			if c := strings.Compare(as[i], bs[i]); c != 0 {
				return c
			}
		}
	}
	return len(as) - len(bs)
}

// isDigits reports whether s is a non-empty run of ASCII digits.
func isDigits(s string) bool {
	return s != "" && strings.TrimLeft(s, "0123456789") == ""
}

// compareDigits compares two ASCII digit strings by numeric value, with no
// size limit: a segment too large for an int still orders as a number.
func compareDigits(a, b string) int {
	a, b = strings.TrimLeft(a, "0"), strings.TrimLeft(b, "0")
	if len(a) != len(b) {
		return len(a) - len(b)
	}
	return strings.Compare(a, b)
}
