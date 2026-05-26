// Package clusterview renders cluster summaries, details, and comparisons.
// It lives in its own package so that both commands/cluster and
// commands/runner can depend on it without an import cycle.
//
// The package is split into focused files:
//   - color.go:   color/status primitives shared by the renderers
//   - list.go:    OutputClustersTable, OutputClustersTree, SortClusterSummaries
//   - detail.go:  OutputClusterDetailsTable
//   - compare.go: OutputComparisonTable
package clusterview
