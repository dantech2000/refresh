package apidoc

// HealthOutcome is what a health check found.
type HealthOutcome string

// The health outcomes.
const (
	// HealthPass: nothing to report.
	HealthPass HealthOutcome = "Pass"
	// HealthWarn: a warning.
	HealthWarn HealthOutcome = "Warn"
	// HealthFail: the check failed. It blocks a change when the check is
	// blocking.
	HealthFail HealthOutcome = "Fail"
	// HealthSkipped: the check did not run, because something it needs
	// is missing or because it does not apply to the cluster.
	HealthSkipped HealthOutcome = "Skipped"
	// HealthUnreadable: a read that the check needs failed. The failure is
	// in the failures list of the document.
	HealthUnreadable HealthOutcome = "Unreadable"
)

// Values implements Enum.
func (HealthOutcome) Values() []string {
	return []string{string(HealthPass), string(HealthWarn), string(HealthFail), string(HealthSkipped), string(HealthUnreadable)}
}

// HealthResult is the result of one health check.
type HealthResult struct {
	Name    string        `json:"name"`
	Outcome HealthOutcome `json:"outcome"`
	// Blocking is true when a Fail of this check blocks a change.
	Blocking bool `json:"blocking"`
	// Message is one line about what the check found.
	Message string   `json:"message"`
	Details []string `json:"details"`
	// Findings name the things behind a Warn or a Fail, such as the nodes
	// that are not ready.
	Findings []string `json:"findings"`
}

// Health is the health part of a describe document.
type Health struct {
	// Summary is the worst outcome of the checks: Fail, then Unreadable,
	// then Warn, then Pass. A Skipped check does not count.
	Summary HealthOutcome  `json:"summary"`
	Checks  []HealthResult `json:"checks"`
}

// GateDecision is the verdict of the health gate of a change.
type GateDecision string

// The gate decisions.
const (
	// GateGo: the change may go on.
	GateGo GateDecision = "Go"
	// GateAsk: the change may go on after a confirmation.
	GateAsk GateDecision = "Ask"
	// GateBlock: the change is stopped.
	GateBlock GateDecision = "Block"
)

// Values implements Enum.
func (GateDecision) Values() []string {
	return []string{string(GateGo), string(GateAsk), string(GateBlock)}
}

// Gate is the verdict of the health gate in a plan or a run.
type Gate struct {
	Decision GateDecision `json:"decision"`
	// Reasons are the checks behind the decision, worst first, as "check:
	// message".
	Reasons []string `json:"reasons"`
	// Accepted are the reasons that --yes went past.
	Accepted []string `json:"accepted"`
	// Forced are the reasons that --force went past.
	Forced []string       `json:"forced"`
	Checks []HealthResult `json:"checks"`
}
