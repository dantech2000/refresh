package change

import (
	"cmp"
	"strconv"
	"strings"
)

// FollowUp holds the words around a command that a notice or a blocker
// names, so that the command reaches the same account, region, and
// Kubernetes context as the run.
type FollowUp struct {
	// Prefix starts the command: "refresh" with the --profile and the
	// --region of the run. Empty means "refresh".
	Prefix string
	// Suffix ends it: the --kubeconfig and the --kube-context of the run.
	Suffix string
	// Resume holds the flags that only the command of a stopped run
	// repeats, and that the request cannot tell from a default: the
	// --wait-timeout that the user chose.
	Resume string
	// Profile is the AWS profile that refresh gives the SDK, from
	// --profile or the active context, for a command of the AWS CLI that a
	// notice names. It is empty when the environment names the profile:
	// the AWS CLI reads the environment too.
	Profile string
}

// FollowUpFor returns the prefix and the suffix of a command that runs in
// the AWS profile and the region, with the kubeconfig and the Kubernetes
// context. An empty value is left out of the command.
func FollowUpFor(profile, region, kubeconfig, kubeContext string) FollowUp {
	prefix := []string{"refresh"}
	if profile != "" {
		prefix = append(prefix, "--profile", shellQuote(profile))
	}
	if region != "" {
		prefix = append(prefix, "--region", shellQuote(region))
	}
	var suffix []string
	if kubeconfig != "" {
		suffix = append(suffix, "--kubeconfig", shellQuote(kubeconfig))
	}
	if kubeContext != "" {
		suffix = append(suffix, "--kube-context", shellQuote(kubeContext))
	}
	return FollowUp{Prefix: strings.Join(prefix, " "), Suffix: strings.Join(suffix, " ")}
}

// Command returns the command line that makes the change of req, with the
// words of f around it. It is the one builder of a refresh command line
// in this package: the command of a dry run that a front end shows, the
// command of a part that a notice names, and the command that resumes a
// run that stopped, since planning again from live state is how a run
// resumes.
//
// The command repeats every flag that decides what the run changes and
// what stops it, so a resume never widens the run and never meets a gate
// that the first run was told to pass: the targets, the parts, the
// exclusions, and --force, --skip-health-check, --require-healthy,
// --skip-verify, and --wait-timeout (in f.Resume). It keeps --yes, so the
// resume of an unattended run is unattended too. The Kubernetes words of
// f.Suffix go only on a command that reads Kubernetes: a roll, an
// upgrade, and a rollback.
func Command(f FollowUp, req Request) string {
	words := []string{cmp.Or(f.Prefix, "refresh")}
	kube := true
	switch r := req.(type) {
	case AddonUpdateRequest:
		kube = false
		words = append(words, "addon", "update")
		words = appendQuoted(words, "", r.Addons)
		if r.All {
			words = append(words, "--all")
		}
		words = append(words, "-c", shellQuote(r.Cluster))
		if r.Version != "" {
			words = append(words, "--version", shellQuote(r.Version))
		}
		words = appendQuoted(words, "--skip-addon", r.Skip)
	case ScaleRequest:
		kube = false
		words = append(words, "nodegroup", "scale", shellQuote(r.Nodegroup), "-c", shellQuote(r.Cluster))
		for _, size := range []struct {
			flag string
			n    *int32
		}{{"--desired", r.Desired}, {"--min", r.Min}, {"--max", r.Max}} {
			if size.n != nil {
				words = append(words, size.flag, strconv.Itoa(int(*size.n)))
			}
		}
	case RollRequest:
		words = append(words, "nodegroup", "update")
		words = appendQuoted(words, "", r.Nodegroups)
		// The regions of a fleet roll are the words of f.Prefix.
		if r.Fleet != nil {
			words = append(words, "--all-clusters")
		} else {
			words = append(words, "-c", shellQuote(r.Cluster))
		}
		// The text of a selection is its -f flags as the user wrote them.
		if r.Select != nil && r.Select.Text != "" {
			words = append(words, r.Select.Text)
		}
		if r.Reroll {
			words = append(words, "--reroll")
		}
	case UpgradeRequest:
		words = append(words, "cluster", "upgrade", shellQuote(r.Cluster), "--to", r.To.String())
		if len(r.Only) > 0 {
			words = append(words, "--only", joinParts(r.Only, ","))
		}
		words = appendQuoted(words, "-n", r.Nodegroups)
		words = appendQuoted(words, "--skip-addon", r.SkipAddons)
		words = appendQuoted(words, "--skip-nodegroup", r.SkipNodegroups)
		if r.SkipInsights {
			words = append(words, "--skip-insights-check")
		}
	case RollbackRequest:
		words = append(words, "cluster", "rollback", shellQuote(r.Cluster))
		words = appendQuoted(words, "--skip-nodegroup", r.SkipNodegroups)
		if r.SkipInsights {
			words = append(words, "--skip-insights-check")
		}
		if r.RollbackTimeout > 0 {
			words = append(words, "--rollback-timeout", r.RollbackTimeout.String())
		}
	}
	c := req.base()
	for _, fl := range []struct {
		set  bool
		name string
	}{
		{c.Policy.Force, "--force"},
		{c.Policy.Skip, "--skip-health-check"},
		{c.Policy.RequireHealthy, "--require-healthy"},
		{c.SkipVerify, "--skip-verify"},
		{c.NoWait, "--no-wait"},
	} {
		if fl.set {
			words = append(words, fl.name)
		}
	}
	if s := strings.TrimSpace(f.Resume); s != "" {
		words = append(words, s)
	}
	if c.Policy.Yes {
		words = append(words, "--yes")
	}
	if s := strings.TrimSpace(f.Suffix); kube && s != "" {
		words = append(words, s)
	}
	return strings.Join(words, " ")
}

// appendQuoted appends each value, quoted, after flag; with no flag, each
// value alone, as an argument.
func appendQuoted(words []string, flag string, values []string) []string {
	for _, v := range values {
		if flag != "" {
			words = append(words, flag)
		}
		words = append(words, shellQuote(v))
	}
	return words
}

// shellQuote returns s as one POSIX shell word: unchanged when it holds
// only safe characters, and in single quotes otherwise. A command that a
// notice prints for the user to run quotes its values with it.
func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	safe := func(r rune) bool {
		return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || strings.ContainsRune("-_./:=,@%+", r)
	}
	if strings.IndexFunc(s, func(r rune) bool { return !safe(r) }) < 0 {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
