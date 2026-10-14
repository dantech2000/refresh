package change

import (
	"slices"

	"github.com/dantech2000/refresh/internal/apidoc"
	"github.com/dantech2000/refresh/internal/health"
)

// Document returns the Plan document of the plan.
func (p *Plan) Document() apidoc.Plan {
	doc := apidoc.Plan{
		Operation:      apidoc.Operation(p.Kind),
		Clusters:       make([]apidoc.ClusterPlan, len(p.Clusters)),
		SkippedRegions: p.SkippedRegions,
		Failures:       p.allFailures(),
	}
	for i := range p.Clusters {
		doc.Clusters[i] = p.Clusters[i].document()
	}
	apidoc.SortFailures(doc.Failures)
	return doc
}

// document returns the document form of the plan of one cluster.
func (cp *ClusterPlan) document() apidoc.ClusterPlan {
	doc := apidoc.ClusterPlan{
		Cluster:       cp.Cluster,
		Region:        cp.Region,
		TargetVersion: cp.To.String(),
		Decision:      cp.Decision(),
		Steps:         []apidoc.PlanStep{},
		Blockers:      []apidoc.Blocker{},
		Notices:       append([]string{}, cp.Notices...),
	}
	if cp.snapshot != nil {
		doc.Version = cp.snapshot.Cluster.Version.String()
	}
	for _, s := range cp.Steps() {
		id := stepID(s.Ref)
		step := apidoc.PlanStep{
			Phase: id.Phase, Hop: id.Hop, Kind: id.Kind, Name: id.Name,
			Action: s.Action, Reason: s.Reason, From: s.From, To: s.To, FromRelease: s.FromRelease, FromImage: s.FromImage, ToImage: s.ToImage, Force: s.Force,
			InFlight: s.InFlight,
		}
		if s.Sizes != nil {
			step.Sizes = &apidoc.ScaleSizes{Before: apidoc.Sizes(s.Sizes.Before), After: apidoc.Sizes(s.Sizes.After)}
		}
		doc.Steps = append(doc.Steps, step)
	}
	for _, b := range cp.Blockers {
		blocker := apidoc.Blocker{Kind: b.Kind, Message: b.Message}
		if b.Step != nil {
			id := stepID(*b.Step)
			blocker.Step = &id
		}
		doc.Blockers = append(doc.Blockers, blocker)
	}
	if cp.Gate.Decision != "" && !cp.Gate.Skipped {
		gate := health.GateDoc(cp.Gate, cp.Checks)
		doc.Gate = &gate
	}
	if cp.Rollback != nil {
		doc.Rollback = &apidoc.Rollback{
			PreviousVersion: cp.Rollback.PreviousVersion.String(),
			UpgradedAt:      cp.Rollback.UpgradedAt,
			AvailableUntil:  cp.Rollback.AvailableUntil,
		}
	}
	return doc
}

// stepID returns the identity of a step in a document.
func stepID(r StepRef) apidoc.StepID {
	return apidoc.StepID{Phase: r.Phase.Kind, Hop: r.Phase.Hop.String(), Kind: r.Kind, Name: r.Name}
}

// Document returns the document of the run: the Plan document for a run
// that ended at its plan, because it was refused or its plan had nothing
// to do, and the Run document for every run that got past its plan. That
// includes a run in which every fresh read before a request found that
// nothing is left to do: its rows say so, and its plan stays the plan
// that was confirmed. A run with no plan has no document, and Document
// returns nil.
func (r Result) Document() apidoc.Document {
	switch {
	case r.Plan == nil:
		return nil
	case r.Status == Refused || (r.Status == NothingToDo && !r.ended.executed):
		return r.Plan.Document()
	}
	doc := apidoc.Run{
		Operation: apidoc.Operation(r.Plan.Kind),
		Status:    r.Status,
		Clusters:  make([]apidoc.ClusterRun, len(r.Clusters)),
		Failures:  slices.Clone(r.Failures),
	}
	for i, c := range r.Clusters {
		run := apidoc.ClusterRun{Status: c.Status, Results: []apidoc.StepResult{}, Verification: health.Documents(c.Verification)}
		if j := slices.IndexFunc(r.Plan.Clusters, func(cp ClusterPlan) bool { return cp.Target == c.Target }); j >= 0 {
			run.ClusterPlan = r.Plan.Clusters[j].document()
		}
		for _, s := range c.Steps {
			id := stepID(s.Ref)
			run.Results = append(run.Results, apidoc.StepResult{
				Phase: id.Phase, Hop: id.Hop, Kind: id.Kind, Name: id.Name,
				Status: s.Status, Attached: s.Attached, UpdateID: s.UpdateID, InFlight: s.InFlight,
				// The clock of the run is local. A document has one zone.
				StartedAt: s.StartedAt.UTC(), FinishedAt: s.FinishedAt.UTC(), Failure: s.Failure, Note: s.Note,
			})
			if s.Failure != nil {
				doc.Failures = append(doc.Failures, *s.Failure)
			}
		}
		doc.Clusters[i] = run
	}
	apidoc.SortFailures(doc.Failures)
	return doc
}
