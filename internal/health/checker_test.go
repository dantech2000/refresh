package health

import (
	"testing"
)

// ---- HealthSummary scoring logic ----
// RunAllChecks hits real AWS, so we test the aggregation logic directly
// by constructing results and verifying the decision rules.

func TestDecision_AllPass(t *testing.T) {
	results := []HealthResult{
		{Status: StatusPass, Score: 100, IsBlocking: false},
		{Status: StatusPass, Score: 90, IsBlocking: false},
	}
	summary := aggregateResults(results)
	if summary.Decision != DecisionProceed {
		t.Errorf("all-pass should be PROCEED, got %s", summary.Decision)
	}
	if summary.OverallScore != 95 {
		t.Errorf("score = %d, want 95", summary.OverallScore)
	}
}

func TestDecision_BlockingFail(t *testing.T) {
	results := []HealthResult{
		{Status: StatusPass, Score: 100, IsBlocking: false},
		{Status: StatusFail, Score: 0, IsBlocking: true, Message: "nodes down"},
	}
	summary := aggregateResults(results)
	if summary.Decision != DecisionBlock {
		t.Errorf("blocking fail should be BLOCK, got %s", summary.Decision)
	}
	if len(summary.Errors) != 1 {
		t.Errorf("expected 1 error, got %d", len(summary.Errors))
	}
}

func TestDecision_NonBlockingFail_GivesWarn(t *testing.T) {
	results := []HealthResult{
		{Status: StatusPass, Score: 100, IsBlocking: false},
		{Status: StatusFail, Score: 30, IsBlocking: false, Message: "partial issue"},
	}
	summary := aggregateResults(results)
	if summary.Decision != DecisionWarn {
		t.Errorf("non-blocking fail should be WARN, got %s", summary.Decision)
	}
}

func TestDecision_Warn_GivesWarn(t *testing.T) {
	results := []HealthResult{
		{Status: StatusPass, Score: 100, IsBlocking: false},
		{Status: StatusWarn, Score: 70, IsBlocking: false, Message: "moderate cpu"},
	}
	summary := aggregateResults(results)
	if summary.Decision != DecisionWarn {
		t.Errorf("warn result should produce WARN decision, got %s", summary.Decision)
	}
	if len(summary.Warnings) != 1 {
		t.Errorf("expected 1 warning, got %d", len(summary.Warnings))
	}
}

func TestDecision_BlockingBeatsWarn(t *testing.T) {
	results := []HealthResult{
		{Status: StatusWarn, Score: 70, IsBlocking: false, Message: "mild warning"},
		{Status: StatusFail, Score: 0, IsBlocking: true, Message: "hard block"},
	}
	summary := aggregateResults(results)
	if summary.Decision != DecisionBlock {
		t.Errorf("blocking fail should override warn, got %s", summary.Decision)
	}
}

// aggregateResults is the real aggregation used by RunAllChecks (defined in
// checker.go); these tests exercise it directly so they don't touch AWS.

func TestAggregate_SkippedCheckExcludedFromScore(t *testing.T) {
	// A perfect measured cluster with one skipped check (no kube client →
	// fixed 70) must not be dragged down: score reflects only measured checks.
	results := []HealthResult{
		{Status: StatusPass, Score: 100, IsBlocking: false},
		{Status: StatusPass, Score: 100, IsBlocking: false},
		{Status: StatusWarn, Score: 70, IsBlocking: false, Skipped: true, Message: "k8s unavailable"},
	}
	summary := aggregateResults(results)
	if summary.OverallScore != 100 {
		t.Errorf("skipped check should be excluded: score = %d, want 100", summary.OverallScore)
	}
	// A skipped WARN still surfaces as a warning in the decision.
	if summary.Decision != DecisionWarn {
		t.Errorf("decision = %s, want WARN", summary.Decision)
	}
}

func TestAggregate_AllSkippedNoPanic(t *testing.T) {
	results := []HealthResult{
		{Status: StatusWarn, Score: 70, Skipped: true},
	}
	summary := aggregateResults(results)
	if summary.OverallScore != 0 {
		t.Errorf("all-skipped score = %d, want 0", summary.OverallScore)
	}
}

// ---- HealthStatus / Decision string values ----

func TestHealthStatusValues(t *testing.T) {
	if StatusPass != "PASS" {
		t.Errorf("StatusPass = %q, want PASS", StatusPass)
	}
	if StatusWarn != "WARN" {
		t.Errorf("StatusWarn = %q, want WARN", StatusWarn)
	}
	if StatusFail != "FAIL" {
		t.Errorf("StatusFail = %q, want FAIL", StatusFail)
	}
}

func TestDecisionValues(t *testing.T) {
	if DecisionProceed != "PROCEED" {
		t.Errorf("DecisionProceed = %q", DecisionProceed)
	}
	if DecisionBlock != "BLOCK" {
		t.Errorf("DecisionBlock = %q", DecisionBlock)
	}
	if DecisionWarn != "WARN" {
		t.Errorf("DecisionWarn = %q", DecisionWarn)
	}
}

// ---- NewChecker accepts nil optional clients ----

func TestNewChecker_NilK8sClient(t *testing.T) {
	hc := NewChecker(nil, nil, nil, nil)
	if hc == nil {
		t.Fatal("NewChecker should not return nil")
	}
}
