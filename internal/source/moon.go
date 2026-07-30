package source

import (
	"math"
	"time"
)

// MoonPhase is one of the eight phases shown on the mirror.
type MoonPhase int

const (
	NewMoon MoonPhase = iota
	WaxingCrescent
	FirstQuarter
	WaxingGibbous
	FullMoon
	WaningGibbous
	LastQuarter
	WaningCrescent
)

// synodicMonth is the mean interval between new moons, in days.
const synodicMonth = 29.530588853

// Name is the human-readable phase name.
func (p MoonPhase) Name() string {
	switch p {
	case WaxingCrescent:
		return "Waxing Crescent"
	case FirstQuarter:
		return "First Quarter"
	case WaxingGibbous:
		return "Waxing Gibbous"
	case FullMoon:
		return "Full Moon"
	case WaningGibbous:
		return "Waning Gibbous"
	case LastQuarter:
		return "Last Quarter"
	case WaningCrescent:
		return "Waning Crescent"
	default:
		return "New Moon"
	}
}

// Icon is the embedded icon name for the phase.
func (p MoonPhase) Icon() string {
	switch p {
	case WaxingCrescent:
		return "moon_waxing_crescent"
	case FirstQuarter:
		return "moon_first_quarter"
	case WaxingGibbous:
		return "moon_waxing_gibbous"
	case FullMoon:
		return "moon_full"
	case WaningGibbous:
		return "moon_waning_gibbous"
	case LastQuarter:
		return "moon_last_quarter"
	case WaningCrescent:
		return "moon_waning_crescent"
	default:
		return "moon_new"
	}
}

// MoonPhaseOn returns the phase for a date.
//
// Ported from v1's ComputeMoonPhase (weather_service.cpp:17). It converts
// the date to a Julian day number, measures the offset from a known new moon
// — JD 2451549.5, 6 January 2000 — and divides the synodic month into eight
// buckets.
//
// Accurate to about a day, which is all an eight-phase icon can express
// anyway: the phases are ~3.7 days wide.
func MoonPhaseOn(t time.Time) MoonPhase {
	y, m, d := t.Date()
	year, month, day := y, int(m), d

	// January and February are treated as months 13 and 14 of the previous
	// year, which is what makes the 30.6001 term below work.
	if month < 3 {
		year--
		month += 12
	}

	a := year / 100
	b := 2 - a + a/4

	jd := int64(365.25*float64(year+4716)) +
		int64(30.6001*float64(month+1)) +
		int64(day) + int64(b) - 1524

	days := math.Mod(float64(jd)-2451549.5, synodicMonth)
	if days < 0 {
		days += synodicMonth
	}

	// Round to the nearest eighth, then wrap: rounding up from the last
	// bucket lands back on new.
	phase := int((days/synodicMonth)*8.0+0.5) & 7
	return MoonPhase(phase)
}
