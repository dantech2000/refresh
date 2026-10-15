package fleetdata

import (
	"fmt"
	"testing"
	"time"
)

// The built-in support calendar has no gap between its minor versions: a
// gap makes a real version unknown. Its dates are in order, and a version
// never leaves a support tier before an older one does.
func TestCalendar_SortedAndComplete(t *testing.T) {
	windows := Calendar()
	if len(windows) == 0 {
		t.Fatal("the calendar is empty")
	}
	var major, minor int
	for i, w := range windows {
		var ma, mi int
		if n, err := fmt.Sscanf(w.Version, "%d.%d", &ma, &mi); n != 2 || err != nil || fmt.Sprintf("%d.%d", ma, mi) != w.Version {
			t.Fatalf("version %q is not a minor version", w.Version)
		}
		if i > 0 && (ma != major || mi != minor+1) {
			t.Errorf("%s follows %d.%d: the calendar is not sorted, or a version is left out", w.Version, major, minor)
		}
		major, minor = ma, mi
		if w.StandardEnd.IsZero() || !w.ExtendedEnd.After(w.StandardEnd) {
			t.Errorf("%s: standard support ends %s and extended support %s; want the second after the first", w.Version, w.StandardEnd.Format(time.DateOnly), w.ExtendedEnd.Format(time.DateOnly))
		}
		// EKS gives a version 12 months of extended support.
		if got := w.StandardEnd.AddDate(1, 0, 0); !got.Equal(w.ExtendedEnd) {
			t.Errorf("%s: extended support ends %s, want a year after standard support (%s)", w.Version, w.ExtendedEnd.Format(time.DateOnly), got.Format(time.DateOnly))
		}
		if i > 0 && (!w.StandardEnd.After(windows[i-1].StandardEnd) || !w.ExtendedEnd.After(windows[i-1].ExtendedEnd)) {
			t.Errorf("%s leaves support before %s does", w.Version, windows[i-1].Version)
		}
	}
}

// The calendar was checked against the EKS release calendar on
// CalendarAsOf, and it covers versions whose support ends after that day.
func TestCalendar_NotStale(t *testing.T) {
	asOf, err := time.Parse(time.DateOnly, CalendarAsOf)
	if err != nil {
		t.Fatalf("CalendarAsOf = %q: %v", CalendarAsOf, err)
	}
	windows := Calendar()
	standard := 0
	for _, w := range windows {
		if w.StandardEnd.After(asOf) {
			standard++
		}
	}
	// EKS keeps at least three versions in standard support.
	if standard < 3 {
		t.Errorf("%d versions are in standard support on %s; the calendar ends too early", standard, CalendarAsOf)
	}
	if first := windows[0]; first.ExtendedEnd.After(asOf) {
		t.Errorf("the oldest version %s is still supported on %s; an older cluster would have an unknown version", first.Version, CalendarAsOf)
	}
}

// The premium is a positive price with a date of its last check.
func TestExtendedSupportPremium(t *testing.T) {
	if _, err := time.Parse(time.DateOnly, PremiumAsOf); err != nil {
		t.Errorf("PremiumAsOf = %q: %v", PremiumAsOf, err)
	}
	if ExtendedSupportPremiumUSDPerHour <= 0 {
		t.Errorf("ExtendedSupportPremiumUSDPerHour = %v, want a positive price", ExtendedSupportPremiumUSDPerHour)
	}
}
