package ics

import (
	"strings"
	"testing"
	"time"
)

func cal(body string) string {
	return "BEGIN:VCALENDAR\r\nVERSION:2.0\r\n" + body + "\r\nEND:VCALENDAR\r\n"
}

func starts(evs []Event) []string {
	out := make([]string, len(evs))
	for i, e := range evs {
		out[i] = e.Start.UTC().Format("Jan 2 15:04")
	}
	return out
}

func within(t *testing.T, from, to string) Options {
	t.Helper()
	return Options{From: mustTime(t, from), To: mustTime(t, to), Loc: time.UTC}
}

// The case that motivated moving to a library.
//
// Every calendar app expresses "I moved one occurrence" as a second VEVENT
// sharing the series UID with a RECURRENCE-ID naming the slot it replaces.
// Handling only the RRULE shows the meeting twice: once where it used to be
// and once where it now is.
func TestRecurrenceIDOverrideMovesRatherThanDuplicates(t *testing.T) {
	res := parse(t, cal(`BEGIN:VEVENT
UID:standup
SUMMARY:Standup
DTSTART:20250602T090000Z
DTEND:20250602T091500Z
RRULE:FREQ=DAILY;COUNT=3
END:VEVENT
BEGIN:VEVENT
UID:standup
RECURRENCE-ID:20250603T090000Z
SUMMARY:Standup
DTSTART:20250603T140000Z
DTEND:20250603T141500Z
END:VEVENT`), within(t, "2025-06-01T00:00:00Z", "2025-06-10T00:00:00Z"))

	got := starts(res.Events)
	want := []string{"Jun 2 09:00", "Jun 3 14:00", "Jun 4 09:00"}

	if len(got) != len(want) {
		t.Fatalf("got %d occurrences %v, want %d %v", len(got), got, len(want), want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("occurrence %d = %s, want %s (full: %v)", i, got[i], want[i], got)
		}
	}
}

// A cancelled single occurrence must vanish without taking the series.
func TestCancelledOccurrenceIsRemoved(t *testing.T) {
	res := parse(t, cal(`BEGIN:VEVENT
UID:standup
SUMMARY:Standup
DTSTART:20250602T090000Z
DTEND:20250602T091500Z
RRULE:FREQ=DAILY;COUNT=3
END:VEVENT
BEGIN:VEVENT
UID:standup
RECURRENCE-ID:20250603T090000Z
STATUS:CANCELLED
DTSTART:20250603T090000Z
END:VEVENT`), within(t, "2025-06-01T00:00:00Z", "2025-06-10T00:00:00Z"))

	got := starts(res.Events)
	if len(got) != 2 {
		t.Fatalf("got %v, want the 3rd removed leaving 2", got)
	}
	for _, s := range got {
		if strings.HasPrefix(s, "Jun 3") {
			t.Errorf("cancelled occurrence still present: %v", got)
		}
	}
}

// BYSETPOS — "the last Friday of each month" — is common in real calendars
// and was not supported by the hand-rolled expander.
func TestBySetPos(t *testing.T) {
	res := parse(t, cal(`BEGIN:VEVENT
UID:retro
SUMMARY:Retro
DTSTART:20250131T160000Z
DTEND:20250131T170000Z
RRULE:FREQ=MONTHLY;BYDAY=MO,TU,WE,TH,FR;BYSETPOS=-1;COUNT=3
END:VEVENT`), within(t, "2025-01-01T00:00:00Z", "2025-05-01T00:00:00Z"))

	got := starts(res.Events)
	want := []string{"Jan 31 16:00", "Feb 28 16:00", "Mar 31 16:00"}
	if len(got) != 3 {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("got %v, want %v", got, want)
			break
		}
	}
}

// RDATE adds one-off dates to a series; also unsupported before.
func TestRDateAddsOccurrences(t *testing.T) {
	res := parse(t, cal(`BEGIN:VEVENT
UID:club
SUMMARY:Club
DTSTART:20250602T180000Z
DTEND:20250602T190000Z
RRULE:FREQ=WEEKLY;COUNT=2
RDATE:20250605T180000Z
END:VEVENT`), within(t, "2025-06-01T00:00:00Z", "2025-06-30T00:00:00Z"))

	if len(res.Events) != 3 {
		t.Fatalf("got %v, want 2 from the rule plus 1 RDATE", starts(res.Events))
	}
}

func TestExDateRemovesOccurrences(t *testing.T) {
	res := parse(t, cal(`BEGIN:VEVENT
UID:standup
SUMMARY:Standup
DTSTART:20250602T090000Z
DTEND:20250602T091500Z
RRULE:FREQ=DAILY;COUNT=4
EXDATE:20250603T090000Z,20250604T090000Z
END:VEVENT`), within(t, "2025-06-01T00:00:00Z", "2025-06-30T00:00:00Z"))

	got := starts(res.Events)
	if len(got) != 2 {
		t.Fatalf("got %v, want 2 after excluding two dates", got)
	}
}

// An event already under way must still be reported, or a meeting vanishes
// from the mirror the moment it starts.
func TestInProgressEventIsIncluded(t *testing.T) {
	res := parse(t, cal(`BEGIN:VEVENT
UID:long
SUMMARY:All afternoon
DTSTART:20250610T120000Z
DTEND:20250610T170000Z
END:VEVENT`), within(t, "2025-06-10T14:00:00Z", "2025-06-11T00:00:00Z"))

	if len(res.Events) != 1 {
		t.Fatalf("got %d events, want the in-progress one", len(res.Events))
	}
}

// A weekly series running for years must not cost anything to window into.
func TestLongRunningSeriesIsBounded(t *testing.T) {
	res := parse(t, cal(`BEGIN:VEVENT
UID:forever
SUMMARY:Weekly
DTSTART:20150105T090000Z
DTEND:20150105T093000Z
RRULE:FREQ=WEEKLY;BYDAY=MO
END:VEVENT`), within(t, "2025-06-01T00:00:00Z", "2025-06-30T00:00:00Z"))

	if n := len(res.Events); n < 4 || n > 5 {
		t.Fatalf("got %d occurrences in June 2025, want 4-5: %v", n, starts(res.Events))
	}
	for _, e := range res.Events {
		if e.Start.Weekday() != time.Monday {
			t.Errorf("%v is a %v, want Monday", e.Start, e.Start.Weekday())
		}
	}
}

// An unbounded rule with no window would expand forever; refuse instead.
func TestUnboundedSeriesWithoutWindowIsSkipped(t *testing.T) {
	res, err := Parse(strings.NewReader(cal(`BEGIN:VEVENT
UID:forever
SUMMARY:Weekly
DTSTART:20250602T090000Z
RRULE:FREQ=WEEKLY
END:VEVENT`)), Options{Loc: time.UTC})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(res.Events) != 0 || res.Skipped != 1 {
		t.Errorf("events=%d skipped=%d, want 0 and 1", len(res.Events), res.Skipped)
	}
}
