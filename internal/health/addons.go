package health

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/dantech2000/refresh/internal/fleet"
)

// Addons checks the add-ons that an add-on update is about to change. It
// makes no call: it reads the add-ons of the snapshot.
//
// An ACTIVE target passes. A DEGRADED target, or one whose create, update,
// or delete failed, is a warning: the update is how such an add-on is
// repaired. A target that EKS is deleting, creating, or updating fails:
// EKS refuses a second change. A target that the snapshot did not read
// makes the check Unreadable.
func Addons(targets []string) Check {
	targets = slices.Clone(targets)
	return Check{
		Name:       "Addons",
		Blocking:   true,
		FailClosed: true,
		Run:        func(_ context.Context, in *Input) Result { return runAddons(in.Snapshot, targets) },
	}
}

func runAddons(snap *fleet.Snapshot, targets []string) Result {
	var warned, failed, unread, details []string
	for _, name := range targets {
		i := slices.IndexFunc(snap.Addons, func(a fleet.Addon) bool { return a.Name == name })
		if i < 0 || snap.Addons[i].State != fleet.Collected {
			unread = append(unread, name)
			continue
		}
		a := snap.Addons[i]
		switch a.Status {
		case "ACTIVE":
		case "DEGRADED", "UPDATE_FAILED", "CREATE_FAILED", "DELETE_FAILED":
			warned = append(warned, name)
			details = append(details, name+" is "+a.Status+issueCodes(a.Issues)+"; the update will try to repair it")
		default:
			failed = append(failed, name)
			details = append(details, name+" is "+a.Status+" and cannot be updated")
		}
	}
	switch {
	case len(unread) > 0:
		return Result{Outcome: Unreadable, Message: "could not read " + strings.Join(unread, ", ")}
	case len(failed) > 0:
		return Result{Outcome: Fail, Message: strings.Join(details, "; "), Findings: sorted(failed, warned)}
	case len(warned) > 0:
		return Result{Outcome: Warn, Message: strings.Join(details, "; "), Findings: sorted(warned)}
	}
	return Result{Outcome: Pass, Message: fmt.Sprintf("%s ACTIVE", plural(len(targets), "add-on"))}
}

// issueCodes returns the codes of the health issues, as " (A, B)", or "".
func issueCodes(issues []fleet.Issue) string {
	var codes []string
	for _, issue := range issues {
		if issue.Code != "" && !slices.Contains(codes, issue.Code) {
			codes = append(codes, issue.Code)
		}
	}
	if len(codes) == 0 {
		return ""
	}
	return " (" + strings.Join(codes, ", ") + ")"
}

// sorted returns the names of the lists in one sorted list.
func sorted(lists ...[]string) []string {
	out := slices.Concat(lists...)
	slices.Sort(out)
	return out
}
