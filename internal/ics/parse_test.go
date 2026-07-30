package ics

import (
	"strings"
	"testing"
	"time"
)

func mustTime(t *testing.T, s string) time.Time {
	t.Helper()
	v, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("bad test time %q: %v", s, err)
	}
	return v
}

// window is a generous span around the test fixtures' dates.
func window(t *testing.T) Options {
	t.Helper()
	return Options{
		From: mustTime(t, "2025-01-01T00:00:00Z"),
		To:   mustTime(t, "2026-01-01T00:00:00Z"),
		Loc:  time.UTC,
	}
}

func parse(t *testing.T, body string, opts Options) Result {
	t.Helper()
	res, err := Parse(strings.NewReader(body), opts)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return res
}

func TestParseSingleEvent(t *testing.T) {
	res := parse(t, `BEGIN:VCALENDAR
BEGIN:VEVENT
UID:abc123
SUMMARY:Dentist
LOCATION:12 High Street
DTSTART:20250610T140000Z
DTEND:20250610T150000Z
END:VEVENT
END:VCALENDAR`, window(t))

	if len(res.Events) != 1 {
		t.Fatalf("got %d events, want 1", len(res.Events))
	}
	e := res.Events[0]
	if e.Summary != "Dentist" {
		t.Errorf("Summary = %q, want %q", e.Summary, "Dentist")
	}
	if e.Location != "12 High Street" {
		t.Errorf("Location = %q", e.Location)
	}
	if want := mustTime(t, "2025-06-10T14:00:00Z"); !e.Start.Equal(want) {
		t.Errorf("Start = %v, want %v", e.Start, want)
	}
	if e.Duration() != time.Hour {
		t.Errorf("Duration = %v, want 1h", e.Duration())
	}
}

// Line folding is the single most damaging thing to get wrong: every long
// SUMMARY in a real feed is folded, so a broken unfolder silently truncates
// most event titles.
func TestParseFoldedLines(t *testing.T) {
	res := parse(t, "BEGIN:VEVENT\r\n"+
		"UID:fold\r\n"+
		"SUMMARY:A very long event title that the\r\n"+
		" \tserver decided to wrap across lines\r\n"+
		"DTSTART:20250610T140000Z\r\n"+
		"END:VEVENT\r\n", window(t))

	if len(res.Events) != 1 {
		t.Fatalf("got %d events, want 1", len(res.Events))
	}
	want := "A very long event title that the\tserver decided to wrap across lines"
	if got := res.Events[0].Summary; got != want {
		t.Errorf("Summary =\n %q\nwant\n %q", got, want)
	}
}

func TestParseAllDay(t *testing.T) {
	res := parse(t, `BEGIN:VEVENT
UID:allday
SUMMARY:Holiday
DTSTART;VALUE=DATE:20250704
END:VEVENT`, window(t))

	if len(res.Events) != 1 {
		t.Fatalf("got %d events, want 1", len(res.Events))
	}
	e := res.Events[0]
	if !e.AllDay {
		t.Error("AllDay = false, want true")
	}
	if e.Duration() != 24*time.Hour {
		t.Errorf("Duration = %v, want 24h", e.Duration())
	}
}

func TestParseEscaping(t *testing.T) {
	res := parse(t, `BEGIN:VEVENT
UID:esc
SUMMARY:Lunch\, then a walk\nBring shoes
DTSTART:20250610T120000Z
END:VEVENT`, window(t))

	want := "Lunch, then a walk\nBring shoes"
	if got := res.Events[0].Summary; got != want {
		t.Errorf("Summary = %q, want %q", got, want)
	}
}

func TestParseCancelledIsSkipped(t *testing.T) {
	res := parse(t, `BEGIN:VEVENT
UID:gone
SUMMARY:Cancelled thing
STATUS:CANCELLED
DTSTART:20250610T120000Z
END:VEVENT`, window(t))

	if len(res.Events) != 0 {
		t.Fatalf("got %d events, want 0", len(res.Events))
	}
}

// A malformed event should cost you that event, not the whole feed.
func TestParseSkipsBadEventKeepsRest(t *testing.T) {
	res := parse(t, `BEGIN:VEVENT
UID:nostart
SUMMARY:Missing DTSTART
END:VEVENT
BEGIN:VEVENT
UID:fine
SUMMARY:Good event
DTSTART:20250610T120000Z
END:VEVENT`, window(t))

	if len(res.Events) != 1 {
		t.Fatalf("got %d events, want 1", len(res.Events))
	}
	if res.Skipped != 1 {
		t.Errorf("Skipped = %d, want 1", res.Skipped)
	}
	if res.Events[0].Summary != "Good event" {
		t.Errorf("wrong event survived: %q", res.Events[0].Summary)
	}
}

func TestParseWindowFiltering(t *testing.T) {
	body := `BEGIN:VEVENT
UID:past
SUMMARY:Long ago
DTSTART:20200101T120000Z
END:VEVENT
BEGIN:VEVENT
UID:inside
SUMMARY:In window
DTSTART:20250610T120000Z
END:VEVENT
BEGIN:VEVENT
UID:future
SUMMARY:Far future
DTSTART:20300101T120000Z
END:VEVENT`

	res := parse(t, body, window(t))
	if len(res.Events) != 1 {
		t.Fatalf("got %d events, want 1", len(res.Events))
	}
	if res.Events[0].Summary != "In window" {
		t.Errorf("kept %q", res.Events[0].Summary)
	}
}

func TestParseSortsByStart(t *testing.T) {
	res := parse(t, `BEGIN:VEVENT
UID:b
SUMMARY:Second
DTSTART:20250610T150000Z
END:VEVENT
BEGIN:VEVENT
UID:a
SUMMARY:First
DTSTART:20250610T090000Z
END:VEVENT`, window(t))

	if len(res.Events) != 2 {
		t.Fatalf("got %d events, want 2", len(res.Events))
	}
	if res.Events[0].Summary != "First" || res.Events[1].Summary != "Second" {
		t.Errorf("wrong order: %q then %q", res.Events[0].Summary, res.Events[1].Summary)
	}
}

// v1 used fixed arrays and silently dropped overflow. Truncation must be
// reported so the UI can say so rather than implying a complete list.
func TestParseTruncationIsReported(t *testing.T) {
	var b strings.Builder
	for i := range 10 {
		b.WriteString("BEGIN:VEVENT\nUID:e")
		b.WriteString(string(rune('a' + i)))
		b.WriteString("\nSUMMARY:Event\nDTSTART:2025061")
		b.WriteString(string(rune('0' + i)))
		b.WriteString("T120000Z\nEND:VEVENT\n")
	}

	opts := window(t)
	opts.Max = 3
	res := parse(t, b.String(), opts)

	if len(res.Events) != 3 {
		t.Fatalf("got %d events, want 3", len(res.Events))
	}
	if !res.Truncated {
		t.Error("Truncated = false, want true")
	}
}

func TestParseDuration(t *testing.T) {
	cases := []struct {
		in   string
		want time.Duration
	}{
		{"PT1H", time.Hour},
		{"PT30M", 30 * time.Minute},
		{"P1D", 24 * time.Hour},
		{"P1DT2H30M", 26*time.Hour + 30*time.Minute},
		{"P1W", 7 * 24 * time.Hour},
		{"PT45S", 45 * time.Second},
		{"garbage", 0},
	}
	for _, c := range cases {
		if got := parseDuration(c.in); got != c.want {
			t.Errorf("parseDuration(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestSplitPropertyHandlesQuotedColon(t *testing.T) {
	name, params, value := splitProperty(`DTSTART;TZID="Europe/London:weird":20250610T140000`)
	if name != "DTSTART" {
		t.Errorf("name = %q", name)
	}
	if params["TZID"] != "Europe/London:weird" {
		t.Errorf("TZID = %q", params["TZID"])
	}
	if value != "20250610T140000" {
		t.Errorf("value = %q", value)
	}
}

func TestParseTZID(t *testing.T) {
	res := parse(t, `BEGIN:VEVENT
UID:tz
SUMMARY:Meeting
DTSTART;TZID=America/Chicago:20250610T090000
END:VEVENT`, window(t))

	if len(res.Events) != 1 {
		t.Fatalf("got %d events, want 1", len(res.Events))
	}
	// 09:00 Chicago in June (CDT, UTC-5) is 14:00 UTC.
	want := mustTime(t, "2025-06-10T14:00:00Z")
	if got := res.Events[0].Start.UTC(); !got.Equal(want) {
		t.Errorf("Start = %v, want %v", got, want)
	}
}

// An unknown TZID must degrade to the default zone, not lose the event.
func TestParseUnknownTZIDFallsBack(t *testing.T) {
	res := parse(t, `BEGIN:VEVENT
UID:badtz
SUMMARY:Meeting
DTSTART;TZID=Mars/Olympus_Mons:20250610T090000
END:VEVENT`, window(t))

	if len(res.Events) != 1 {
		t.Fatalf("got %d events, want 1", len(res.Events))
	}
}
