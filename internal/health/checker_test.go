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

// aggregateResults mirrors the logic in RunAllChecks so we can unit-test it
// without touching AWS. Keep it in sync if RunAllChecks logic changes.
func aggregateResults(results []HealthResult) HealthSummary {
	var warnings, errors []string
	totalScore := 0
	hasBlocking := false
	hasWarnings := false

	for _, r := range results {
		totalScore += r.Score
		if r.Status == StatusFail && r.IsBlocking {
			hasBlocking = true
			errors = append(errors, r.Message)
		} else if r.Status == StatusWarn {
			hasWarnings = true
			warnings = append(warnings, r.Message)
		} else if r.Status == StatusFail {
			errors = append(errors, r.Message)
			hasWarnings = true
		}
	}

	decision := DecisionProceed
	if hasBlocking {
		decision = DecisionBlock
	} else if hasWarnings || len(errors) > 0 {
		decision = DecisionWarn
	}

	return HealthSummary{
		Results:      results,
		OverallScore: totalScore / len(results),
		Decision:     decision,
		Warnings:     warnings,
		Errors:       errors,
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
