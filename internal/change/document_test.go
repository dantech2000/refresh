package change

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/dantech2000/refresh/internal/apidoc"
)

// The clock of a run is the local one. The times of a document are UTC,
// as the times of the table are, whatever the zone of the machine.
func TestDocument_TimesAreUTC(t *testing.T) {
	zone := time.FixedZone("UTC-7", -7*60*60)
	started := time.Date(2026, 5, 1, 5, 0, 0, 0, zone)
	ref := StepRef{Phase: PhaseRef{Target: Target{Region: east, Cluster: "prod"}, Kind: PhaseRoll}, Kind: StepNodegroup, Name: "ng-a"}
	res := Result{
		Status: Succeeded,
		Plan: &Plan{Kind: NodegroupUpdate, Clusters: []ClusterPlan{{
			Target: ref.Phase.Target, Phases: []Phase{{Ref: ref.Phase, Steps: []Step{{Ref: ref, Action: Submit}}}},
		}}},
		Clusters: []ClusterResult{{Target: ref.Phase.Target, Status: Succeeded, Steps: []StepResult{
			{Ref: ref, Status: StepSucceeded, StartedAt: started, FinishedAt: started.Add(time.Minute)},
			{Ref: ref, Status: StepNotAttempted},
		}}},
	}
	doc, ok := res.Document().(apidoc.Run)
	if !ok {
		t.Fatalf("document = %T, want a Run", res.Document())
	}
	row := doc.Clusters[0].Results[0]
	if row.StartedAt.Location() != time.UTC || row.FinishedAt.Location() != time.UTC {
		t.Errorf("startedAt in %s, finishedAt in %s; want UTC", row.StartedAt.Location(), row.FinishedAt.Location())
	}
	if !row.StartedAt.Equal(started) {
		t.Errorf("startedAt = %s, want the same moment as %s", row.StartedAt, started)
	}
	data, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"startedAt":"2026-05-01T12:00:00Z"`) || strings.Contains(string(data), "-07:00") {
		t.Errorf("the JSON has a local offset: %s", data)
	}
	// A step that never started has no time, and not the zero time in UTC.
	if strings.Count(string(data), "startedAt") != 1 {
		t.Errorf("a step that was not attempted has a startedAt: %s", data)
	}
}

// The message of an interrupted run says "interrupted", and never the
// words of the cancelled context, wherever the message goes on after it.
// It is still that context for errors.Is.
func TestInterruptMessage(t *testing.T) {
	steps := []StepResult{{Status: StepInProgress, Failure: &apidoc.Failure{Reason: apidoc.ReasonInterrupted, Error: "interrupted while the update of nodegroup ng-a was running"}}}
	res := Result{Plan: &Plan{Kind: ClusterUpgrade}, Clusters: []ClusterResult{{Steps: steps}}, ended: ending{interrupted: true}}
	err := fmt.Errorf("%w; no phase completed", summary(res))
	if !errors.Is(err, context.Canceled) {
		t.Errorf("%v is not the cancelled context", err)
	}
	if msg := err.Error(); strings.Contains(msg, "context canceled") || !strings.HasPrefix(msg, "interrupted: ") {
		t.Errorf("message = %q, want it to say interrupted and not %q", msg, "context canceled")
	}
}
