package ics

import (
	"strings"
	"testing"
	"time"
)

// starts renders occurrence times as YYYY-MM-DD for readable failures.
func starts(evs []Event) []string {
	out := make([]string, len(evs))
	for i, e := range evs {
		out[i] = e.Start.Format("2006-01-02")
	}
	return out
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func parseRecurring(t *testing.T, rule, dtstart string, from, to string) []Event {
	t.Helper()
	body := "BEGIN:VEVENT\nUID:r\nSUMMARY:Repeat\nDTSTART:" + dtstart + "\nRRULE:" + rule + "\nEND:VEVENT\n"
	res, err := Parse(strings.NewReader(body), Options{
		From: mustTime(t, from),
		To:   mustTime(t, to),
		Loc:  time.UTC,
	})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return res.Events
}

func TestRRuleDaily(t *testing.T) {
	evs := parseRecurring(t, "FREQ=DAILY;COUNT=4",
		"20250610T090000Z", "2025-06-01T00:00:00Z", "2025-07-01T00:00:00Z")

	want := []string{"2025-06-10", "2025-06-11", "2025-06-12", "2025-06-13"}
	if got := starts(evs); !equalStrings(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestRRuleDailyInterval(t *testing.T) {
	evs := parseRecurring(t, "FREQ=DAILY;INTERVAL=3;COUNT=3",
		"20250610T090000Z", "2025-06-01T00:00:00Z", "2025-07-01T00:00:00Z")

	want := []string{"2025-06-10", "2025-06-13", "2025-06-16"}
	if got := starts(evs); !equalStrings(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestRRuleWeeklyByDay(t *testing.T) {
	// 2025-06-10 is a Tuesday. Every Mon/Wed for two weeks.
	//
	// UNTIL is compared against each occurrence's start time, so it is set
	// past 09:00 on the final day — an UNTIL of midnight on the 25th would
	// correctly exclude that day's 09:00 occurrence.
	evs := parseRecurring(t, "FREQ=WEEKLY;BYDAY=MO,WE;UNTIL=20250625T120000Z",
		"20250610T090000Z", "2025-06-01T00:00:00Z", "2025-07-01T00:00:00Z")

	want := []string{
		"2025-06-11", // Wed of the DTSTART week (Mon 09 precedes DTSTART)
		"2025-06-16", "2025-06-18",
		"2025-06-23", "2025-06-25",
	}
	if got := starts(evs); !equalStrings(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestRRuleUntilBound(t *testing.T) {
	evs := parseRecurring(t, "FREQ=DAILY;UNTIL=20250613T000000Z",
		"20250610T090000Z", "2025-06-01T00:00:00Z", "2025-07-01T00:00:00Z")

	want := []string{"2025-06-10", "2025-06-11", "2025-06-12"}
	if got := starts(evs); !equalStrings(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestRRuleMonthlyByMonthDay(t *testing.T) {
	evs := parseRecurring(t, "FREQ=MONTHLY;BYMONTHDAY=15;COUNT=3",
		"20250115T090000Z", "2025-01-01T00:00:00Z", "2026-01-01T00:00:00Z")

	want := []string{"2025-01-15", "2025-02-15", "2025-03-15"}
	if got := starts(evs); !equalStrings(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

// Negative BYMONTHDAY counts back from the end, and February makes the
// arithmetic interesting.
func TestRRuleMonthlyLastDay(t *testing.T) {
	evs := parseRecurring(t, "FREQ=MONTHLY;BYMONTHDAY=-1;COUNT=3",
		"20250131T090000Z", "2025-01-01T00:00:00Z", "2026-01-01T00:00:00Z")

	want := []string{"2025-01-31", "2025-02-28", "2025-03-31"}
	if got := starts(evs); !equalStrings(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestRRuleMonthlyNthWeekday(t *testing.T) {
	// Third Monday of each month.
	evs := parseRecurring(t, "FREQ=MONTHLY;BYDAY=3MO;COUNT=3",
		"20250120T090000Z", "2025-01-01T00:00:00Z", "2026-01-01T00:00:00Z")

	want := []string{"2025-01-20", "2025-02-17", "2025-03-17"}
	if got := starts(evs); !equalStrings(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestRRuleMonthlyLastWeekday(t *testing.T) {
	// Last Friday of each month.
	evs := parseRecurring(t, "FREQ=MONTHLY;BYDAY=-1FR;COUNT=3",
		"20250131T090000Z", "2025-01-01T00:00:00Z", "2026-01-01T00:00:00Z")

	want := []string{"2025-01-31", "2025-02-28", "2025-03-28"}
	if got := starts(evs); !equalStrings(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestRRuleYearly(t *testing.T) {
	evs := parseRecurring(t, "FREQ=YEARLY;COUNT=3",
		"20250704T090000Z", "2025-01-01T00:00:00Z", "2029-01-01T00:00:00Z")

	want := []string{"2025-07-04", "2026-07-04", "2027-07-04"}
	if got := starts(evs); !equalStrings(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestRRuleEXDATE(t *testing.T) {
	body := `BEGIN:VEVENT
UID:ex
SUMMARY:Standup
DTSTART:20250610T090000Z
RRULE:FREQ=DAILY;COUNT=4
EXDATE:20250611T090000Z,20250613T090000Z
END:VEVENT`

	res, err := Parse(strings.NewReader(body), Options{
		From: mustTime(t, "2025-06-01T00:00:00Z"),
		To:   mustTime(t, "2025-07-01T00:00:00Z"),
		Loc:  time.UTC,
	})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	want := []string{"2025-06-10", "2025-06-12"}
	if got := starts(res.Events); !equalStrings(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

// A weekly event that started years ago must not cost thousands of
// iterations to reach today's window.
func TestRRuleFastForwardsOldSeries(t *testing.T) {
	evs := parseRecurring(t, "FREQ=WEEKLY;BYDAY=MO",
		"20150105T090000Z", "2025-06-01T00:00:00Z", "2025-06-30T00:00:00Z")

	if len(evs) < 4 || len(evs) > 5 {
		t.Fatalf("got %d occurrences in June 2025, want 4-5: %v", len(evs), starts(evs))
	}
	for _, e := range evs {
		if e.Start.Weekday() != time.Monday {
			t.Errorf("occurrence %v is a %v, want Monday", e.Start, e.Start.Weekday())
		}
	}
}

// An unbounded rule with no window would expand forever; refuse instead.
func TestRRuleUnboundedWithoutWindowIsRejected(t *testing.T) {
	_, err := expandRRULE("FREQ=DAILY",
		mustTime(t, "2025-06-10T09:00:00Z"), time.Time{}, time.Time{})
	if err == nil {
		t.Fatal("expected an error for an unbounded rule with no window")
	}
}

// Sub-daily frequencies would be millions of occurrences over a 90-day
// window and have no place on a mirror.
func TestRRuleRejectsSubDailyFreq(t *testing.T) {
	for _, freq := range []string{"SECONDLY", "MINUTELY", "HOURLY"} {
		if _, err := parseRRULE("FREQ=" + freq); err == nil {
			t.Errorf("FREQ=%s was accepted, want rejection", freq)
		}
	}
}

func TestParseWeekdayNum(t *testing.T) {
	cases := []struct {
		in     string
		ord    int
		day    time.Weekday
		wantOK bool
	}{
		{"MO", 0, time.Monday, true},
		{"3MO", 3, time.Monday, true},
		{"-1FR", -1, time.Friday, true},
		{"SU", 0, time.Sunday, true},
		{"XX", 0, 0, false},
		{"", 0, 0, false},
	}
	for _, c := range cases {
		got, ok := parseWeekdayNum(c.in)
		if ok != c.wantOK {
			t.Errorf("parseWeekdayNum(%q) ok = %v, want %v", c.in, ok, c.wantOK)
			continue
		}
		if ok && (got.ord != c.ord || got.day != c.day) {
			t.Errorf("parseWeekdayNum(%q) = {%d %v}, want {%d %v}",
				c.in, got.ord, got.day, c.ord, c.day)
		}
	}
}
