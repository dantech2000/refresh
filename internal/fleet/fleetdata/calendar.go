package fleetdata

import "time"

// CalendarAsOf is the date of the last check of the support calendar
// against the EKS release calendar.
const CalendarAsOf = "2026-10-12"

// SupportWindow is when the support of a Kubernetes version on EKS ends.
type SupportWindow struct {
	// Version is the Kubernetes minor version, such as "1.33".
	Version string
	// StandardEnd is the end of standard support, and ExtendedEnd the end
	// of extended support.
	StandardEnd, ExtendedEnd time.Time
}

// Calendar returns the support windows that refresh ships with, the oldest
// version first and with no version left out between the first and the
// last. A command uses it when EKS does not answer
// DescribeClusterVersions. The dates are from the EKS release calendar:
// https://docs.aws.amazon.com/eks/latest/userguide/kubernetes-versions.html
func Calendar() []SupportWindow {
	day := func(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 0, 0, 0, 0, time.UTC) }
	return []SupportWindow{
		{"1.28", day(2024, 11, 26), day(2025, 11, 26)},
		{"1.29", day(2025, 3, 23), day(2026, 3, 23)},
		{"1.30", day(2025, 7, 23), day(2026, 7, 23)},
		{"1.31", day(2025, 11, 26), day(2026, 11, 26)},
		{"1.32", day(2026, 3, 23), day(2027, 3, 23)},
		{"1.33", day(2026, 7, 29), day(2027, 7, 29)},
		{"1.34", day(2026, 12, 2), day(2027, 12, 2)},
		{"1.35", day(2027, 3, 27), day(2028, 3, 27)},
		{"1.36", day(2027, 8, 2), day(2028, 8, 2)},
		{"1.37", day(2027, 12, 1), day(2028, 12, 1)},
	}
}
