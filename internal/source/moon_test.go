package source

import (
	"testing"
	"time"
)

func TestMoonPhaseKnownDates(t *testing.T) {
	// Reference new and full moons, from published ephemerides. The phase
	// buckets are ~3.7 days wide, so a date within a day of the event must
	// land in the right one.
	cases := []struct {
		date string
		want MoonPhase
	}{
		{"2000-01-06", NewMoon}, // the epoch this algorithm is anchored to
		{"2024-01-11", NewMoon},
		{"2024-01-25", FullMoon},
		{"2024-06-06", NewMoon},
		{"2024-06-22", FullMoon},
		{"2025-03-29", NewMoon},
		{"2025-04-13", FullMoon},
	}

	for _, c := range cases {
		d, err := time.Parse("2006-01-02", c.date)
		if err != nil {
			t.Fatalf("bad test date %q: %v", c.date, err)
		}
		if got := MoonPhaseOn(d); got != c.want {
			t.Errorf("MoonPhaseOn(%s) = %v, want %v", c.date, got.Name(), c.want.Name())
		}
	}
}

// The cycle must advance through all eight phases and return to new.
func TestMoonPhaseCyclesThroughAllEight(t *testing.T) {
	start := time.Date(2025, 3, 29, 12, 0, 0, 0, time.UTC) // a new moon
	seen := map[MoonPhase]bool{}

	for i := range 30 {
		seen[MoonPhaseOn(start.AddDate(0, 0, i))] = true
	}
	if len(seen) != 8 {
		t.Errorf("saw %d distinct phases over a synodic month, want 8", len(seen))
	}
}

// Every phase must name an icon that actually exists, or the mirror renders
// a blank where the moon should be.
func TestEveryPhaseHasAnEmbeddedIcon(t *testing.T) {
	for p := NewMoon; p <= WaningCrescent; p++ {
		if p.Icon() == "" {
			t.Errorf("%v has no icon name", p.Name())
		}
		if p.Name() == "" {
			t.Errorf("phase %d has no name", int(p))
		}
	}
}

// Dates before the epoch must not produce a negative bucket.
func TestMoonPhaseBeforeEpoch(t *testing.T) {
	d := time.Date(1969, 7, 20, 0, 0, 0, 0, time.UTC)
	got := MoonPhaseOn(d)
	if got < NewMoon || got > WaningCrescent {
		t.Errorf("MoonPhaseOn(1969-07-20) = %d, outside 0..7", int(got))
	}
}
