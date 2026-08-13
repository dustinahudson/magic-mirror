package ics

import (
	"runtime"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// A recurrence rule arrives from whatever URL somebody pasted into the
// settings page, and RFC 5545 allows rules that expand to millions of
// occurrences. This device has 512MB, no swap, and one core.
//
// The failure is not a slow calendar. The kernel kills the process, the
// supervisor counts a failure, and three of those roll the mirror back to a
// build with the same feed still configured — from a house nobody can visit.

func feedWith(rule, dtstart string) string {
	return strings.Join([]string{
		"BEGIN:VCALENDAR",
		"VERSION:2.0",
		"BEGIN:VEVENT",
		"UID:pathological@example.invalid",
		"DTSTART:" + dtstart,
		"DTEND:" + dtstart,
		"SUMMARY:every second forever",
		"RRULE:" + rule,
		"END:VEVENT",
		"END:VCALENDAR",
		"",
	}, "\r\n")
}

func deviceWindow(t *testing.T) Options {
	t.Helper()
	now := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	return Options{
		From: now.Add(-24 * time.Hour),
		To:   now.Add(90 * 24 * time.Hour), // the window the device actually uses
		Loc:  time.UTC,
		Max:  500,
	}
}

// FREQ=SECONDLY across ninety days is nearly eight million occurrences.
// Materialising them costs about 190MB of time.Time before anything consults
// a cap, and the result slice used to be preallocated to match.
func TestSecondlyRuleDoesNotExhaustMemory(t *testing.T) {
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)

	res, err := Parse(strings.NewReader(feedWith("FREQ=SECONDLY", "20260811T120000Z")), deviceWindow(t))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	runtime.GC()
	runtime.ReadMemStats(&after)

	// The cap is 2000 occurrences per series; anything near eight million
	// means the walk materialised first and capped afterwards.
	if len(res.Events) > occurrenceCap {
		t.Errorf("produced %d events, want at most %d", len(res.Events), occurrenceCap)
	}

	// Total allocation across the parse, not live heap: the point is that the
	// peak was never reached, and a materialised slice would show here even
	// after collection.
	allocated := after.TotalAlloc - before.TotalAlloc
	const budget = 64 << 20
	if allocated > budget {
		t.Errorf("allocated %dMB parsing one rule; a 512MB device has no room for that",
			allocated>>20)
	}
	t.Logf("%d events, %dKB allocated", len(res.Events), allocated>>10)
}

// The same rule with a start decades in the past has to be walked to reach
// the window at all. Bounded, or it burns the only core the device has.
func TestSecondlyRuleFromLongAgoTerminates(t *testing.T) {
	done := make(chan int, 1)
	go func() {
		res, err := Parse(strings.NewReader(
			feedWith("FREQ=SECONDLY", "19700101T000000Z")), deviceWindow(t))
		if err != nil {
			done <- -1
			return
		}
		done <- len(res.Events)
	}()

	select {
	case n := <-done:
		if n > occurrenceCap {
			t.Errorf("produced %d events, want at most %d", n, occurrenceCap)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("still expanding after 30s; the walk is not bounded")
	}
}

// Minutely is less extreme but still 130,000 occurrences across the window,
// and is the sort of thing a badly configured sync tool really produces.
func TestMinutelyRuleIsCapped(t *testing.T) {
	res, err := Parse(strings.NewReader(
		feedWith("FREQ=MINUTELY", "20260811T120000Z")), deviceWindow(t))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(res.Events) > occurrenceCap {
		t.Errorf("produced %d events, want at most %d", len(res.Events), occurrenceCap)
	}
}

// The caps must not damage ordinary calendars, which is the whole reason the
// device exists. A weekly meeting across ninety days is thirteen occurrences
// and every one of them must survive.
func TestOrdinaryRecurrenceIsUntouched(t *testing.T) {
	res, err := Parse(strings.NewReader(
		feedWith("FREQ=WEEKLY", "20260811T120000Z")), deviceWindow(t))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(res.Events) < 12 || len(res.Events) > 14 {
		t.Errorf("weekly across ninety days gave %d events, want about 13", len(res.Events))
	}
}

// A daily event that started decades ago is completely normal — an
// anniversary, a bin collection — and must still appear.
func TestLongRunningDailyRuleStillReachesTheWindow(t *testing.T) {
	res, err := Parse(strings.NewReader(
		feedWith("FREQ=DAILY", "20000101T090000Z")), deviceWindow(t))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(res.Events) == 0 {
		t.Fatal("a daily rule from 2000 produced nothing inside the window")
	}
	for _, e := range res.Events {
		if e.Start.Year() != 2026 {
			t.Errorf("event outside the window: %v", e.Start)
			break
		}
	}
}

// Titles and locations come out of a feed and are measured, drawn, and copied
// into a change-detection key on every repaint. Nothing in ICS promises they
// are short — a description pasted into a summary runs to kilobytes — and the
// cost of that lands once a second on one ARMv6 core.
func TestOversizedTitlesAreClipped(t *testing.T) {
	huge := strings.Repeat("A", 100_000)
	feed := strings.Join([]string{
		"BEGIN:VCALENDAR", "VERSION:2.0", "BEGIN:VEVENT",
		"UID:wordy@example.invalid",
		"DTSTART:20260812T120000Z",
		"DTEND:20260812T130000Z",
		"SUMMARY:" + huge,
		"LOCATION:" + huge,
		"END:VEVENT", "END:VCALENDAR", "",
	}, "\r\n")

	res, err := Parse(strings.NewReader(feed), deviceWindow(t))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(res.Events) != 1 {
		t.Fatalf("got %d events, want 1", len(res.Events))
	}

	e := res.Events[0]
	if len(e.Summary) > maxTextLen+8 {
		t.Errorf("summary is %d bytes; it was never clipped", len(e.Summary))
	}
	if len(e.Location) > maxTextLen+8 {
		t.Errorf("location is %d bytes; it was never clipped", len(e.Location))
	}
}

// Clipping must not produce a broken final character, or the renderer draws a
// replacement glyph and the key carries invalid UTF-8.
func TestClippingLandsOnARuneBoundary(t *testing.T) {
	// Three-byte runes, so a naive cut at 512 bytes lands mid-character.
	body := strings.Repeat("あ", 1000)
	got := clip(body)
	if !utf8.ValidString(got) {
		t.Errorf("clipped text is not valid UTF-8: %q", got[len(got)-8:])
	}
	if len(got) > maxTextLen+8 {
		t.Errorf("clipped to %d bytes, want about %d", len(got), maxTextLen)
	}
}

// Ordinary titles must pass through untouched, including the trailing
// character — an off-by-one here would clip every event title in the world.
func TestOrdinaryTitlesAreUntouched(t *testing.T) {
	for _, s := range []string{"", "Standup", "Dentist — 3pm, bring the form", "café ☕"} {
		if got := clip(s); got != s {
			t.Errorf("clip(%q) = %q, want it unchanged", s, got)
		}
	}
}
