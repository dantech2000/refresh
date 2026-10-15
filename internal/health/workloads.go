package health

import (
	"context"
	"fmt"
	"slices"
	"strings"

	corev1 "k8s.io/api/core/v1"

	"github.com/dantech2000/refresh/internal/apidoc"
)

// criticalNamespaces hold the pods that every node needs.
var criticalNamespaces = []string{"kube-system", "kube-public", "kube-node-lease"}

// minRunningPodPercent is the share of the critical pods that must be
// Running and ready for a warning rather than a failure.
const minRunningPodPercent = 90

// kubeletShutdownMessage is what the kubelet writes on a pod it ended for
// a node shutdown.
const kubeletShutdownMessage = "Pod was terminated in response to imminent node shutdown."

// Workloads checks that the pods of the system namespaces are Running
// with every container ready. A Succeeded pod, and a Failed pod that an
// eviction or a node shutdown left behind, do not count: they stay until
// garbage collection and say nothing about their replacements.
func Workloads() Check {
	return Check{Name: "Workloads", Blocking: true, FailClosed: true, Needs: Needs{Kube: true}, Run: runWorkloads}
}

func runWorkloads(_ context.Context, in *Input) Result {
	var (
		total, running int
		problems       []string
		details        []string
		failures       []apidoc.Failure
		unread         []string
	)
	// The three lists in one round trip.
	together(
		func() { _, _ = in.reads.Pods(criticalNamespaces[0]) },
		func() { _, _ = in.reads.Pods(criticalNamespaces[1]) },
		func() { _, _ = in.reads.Pods(criticalNamespaces[2]) },
	)
	for _, ns := range criticalNamespaces {
		pods, err := in.reads.Pods(ns)
		if err != nil {
			failures = append(failures, kubeFailure(in.Snapshot.Cluster.Name, "listing pods in "+ns, err))
			unread = append(unread, ns)
			continue
		}
		nsTotal, nsRunning := 0, 0
		for i := range pods.Items {
			pod := &pods.Items[i]
			if pod.Status.Phase == corev1.PodSucceeded || staleFailedPod(pod) {
				continue
			}
			nsTotal++
			if problem := podProblem(pod); problem == "" {
				nsRunning++
			} else {
				problems = append(problems, ns+"/"+pod.Name+" ("+problem+")")
			}
		}
		total += nsTotal
		running += nsRunning
		details = append(details, fmt.Sprintf("%s: %d/%d pods running", ns, nsRunning, nsTotal))
	}
	r := evaluateWorkloads(total, running, problems)
	r.Details = append(details, r.Details...)
	if len(unread) == 0 {
		return r
	}
	r.Failures = failures
	if r.Outcome != Fail {
		r.Outcome = Unreadable
	}
	r.Message += "; not read: " + strings.Join(unread, ", ")
	return r
}

// evaluateWorkloads is the rule, apart from the reads.
func evaluateWorkloads(total, running int, problems []string) Result {
	findings := make([]string, 0, len(problems))
	for _, p := range problems {
		name, _, _ := strings.Cut(p, " (")
		findings = append(findings, name)
	}
	slices.Sort(findings)
	r := Result{Findings: findings}
	switch {
	case total == 0:
		r.Outcome, r.Message = Warn, "no pods in the system namespaces"
	case len(problems) == 0:
		r.Outcome, r.Message = Pass, fmt.Sprintf("%d/%d pods running in the system namespaces", running, total)
	case running*100 >= total*minRunningPodPercent:
		r.Outcome, r.Message = Warn, fmt.Sprintf("%d/%d system pods running; %s", running, total, strings.Join(problems, ", "))
	default:
		r.Outcome, r.Message = Fail, fmt.Sprintf("%d/%d system pods running; %s", running, total, strings.Join(problems, ", "))
	}
	return r
}

// podProblem says what is wrong with a pod, or "" for a Running pod whose
// containers are all ready.
func podProblem(pod *corev1.Pod) string {
	if pod.Status.Phase != corev1.PodRunning {
		return string(pod.Status.Phase)
	}
	for _, c := range pod.Status.ContainerStatuses {
		if !c.Ready {
			return "containers not ready"
		}
	}
	return ""
}

// staleFailedPod reports whether a Failed pod was left behind by an
// eviction or a node shutdown. "Terminated" is a generic reason, so it
// counts only with the DisruptionTarget condition of the kubelet or its
// shutdown message.
func staleFailedPod(pod *corev1.Pod) bool {
	if pod.Status.Phase != corev1.PodFailed {
		return false
	}
	switch pod.Status.Reason {
	case "Evicted", "Shutdown", "NodeShutdown":
		return true
	case "Terminated":
		for _, c := range pod.Status.Conditions {
			if c.Type == corev1.DisruptionTarget && c.Status == corev1.ConditionTrue && c.Reason == corev1.PodReasonTerminationByKubelet {
				return true
			}
		}
		return pod.Status.Message == kubeletShutdownMessage
	}
	return false
}
