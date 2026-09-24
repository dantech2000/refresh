package addon

import (
	"github.com/dantech2000/refresh/internal/diag"
	"github.com/dantech2000/refresh/internal/services/addons"
)

// addonUpdateDocument is the -o json/yaml document of a single add-on
// update: the result's fields, plus the run's failures.
type addonUpdateDocument struct {
	addons.AddonUpdateResult
	Failures diag.List `json:"failures" yaml:"failures"`
}

// addonUpdateAllDocument is the -o json/yaml document of
// `addon update --all`.
type addonUpdateAllDocument struct {
	Cluster  string                     `json:"cluster" yaml:"cluster"`
	DryRun   bool                       `json:"dryRun" yaml:"dryRun"`
	Results  []addons.AddonUpdateResult `json:"results" yaml:"results"`
	Failures diag.List                  `json:"failures" yaml:"failures"`
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
