package addon

import (
	"github.com/dantech2000/refresh/internal/apidoc"
	"github.com/dantech2000/refresh/internal/commands/runner"
	"github.com/dantech2000/refresh/internal/diag"
	"github.com/dantech2000/refresh/internal/services/addons"
)

// addonUpdateDocument is the -o json/yaml document of a single add-on
// update: the result's fields, plus the run's failures.
type addonUpdateDocument struct {
	addons.AddonUpdateResult
	// ChangesInProgress names what EKS was changing on the cluster when the
	// update refused to start (Status Busy). Left out otherwise.
	ChangesInProgress []string  `json:"changesInProgress,omitempty" yaml:"changesInProgress,omitempty"`
	Failures          diag.List `json:"failures" yaml:"failures"`
}

// DocumentKind is AddonUpdate.
func (addonUpdateDocument) DocumentKind() apidoc.Kind { return apidoc.KindAddonUpdate }

// addonUpdateAllDocument is the -o json/yaml document of
// `addon update --all`.
type addonUpdateAllDocument struct {
	Cluster string                     `json:"cluster" yaml:"cluster"`
	DryRun  bool                       `json:"dryRun" yaml:"dryRun"`
	Results []addons.AddonUpdateResult `json:"results" yaml:"results"`
	// ChangesInProgress names what EKS was changing on the cluster when the
	// run refused to start (exit 3, no results). Left out otherwise.
	ChangesInProgress []string  `json:"changesInProgress,omitempty" yaml:"changesInProgress,omitempty"`
	Failures          diag.List `json:"failures" yaml:"failures"`
}

// DocumentKind is AddonUpdateAll.
func (addonUpdateAllDocument) DocumentKind() apidoc.Kind { return apidoc.KindAddonUpdateAll }

// refusalFailures is the failure list of a refusal: the failed read, if any.
func refusalFailures(busy *runner.Busy) diag.List {
	if busy.Failure == nil {
		return diag.List{}
	}
	return diag.List{*busy.Failure}
}

// refuseOne ends a single add-on update that refused to start: -o json|yaml
// prints its document (Status Busy, or NotAttempted with the failed read),
// then the exit 3 error.
func refuseOne(format, addonName string, busy *runner.Busy) error {
	if runner.IsMachineFormat(format) {
		res := addons.AddonUpdateResult{AddonName: addonName, Status: addons.StatusBusy}
		if busy.Failure != nil {
			res.Status, res.Failure = addons.StatusNotAttempted, busy.Failure
		}
		doc := addonUpdateDocument{AddonUpdateResult: res, ChangesInProgress: busy.Changes, Failures: refusalFailures(busy)}
		if _, err := runner.EncodeStdout(format, doc); err != nil {
			return err
		}
	}
	return busy.Exit
}

// refuseAll ends `addon update --all` that refused to start: -o json|yaml
// prints its document with no results, then the exit 3 error.
func refuseAll(format, cluster string, busy *runner.Busy) error {
	if runner.IsMachineFormat(format) {
		doc := addonUpdateAllDocument{Cluster: cluster, Results: []addons.AddonUpdateResult{}, ChangesInProgress: busy.Changes, Failures: refusalFailures(busy)}
		if _, err := runner.EncodeStdout(format, doc); err != nil {
			return err
		}
	}
	return busy.Exit
}

// resultFailures sets region on each result's failure (the service does not
// know it) and returns the failures, sorted.
func resultFailures(results []addons.AddonUpdateResult, region string) diag.List {
	var fs diag.List
	for i := range results {
		f := results[i].Failure
		if f == nil {
			continue
		}
		if f.Region == "" {
			f.Region = region
		}
		fs = append(fs, *f)
	}
	diag.Sort(fs)
	return fs
}
