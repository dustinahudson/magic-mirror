package ics

import (
	"testing"
	"time"
)

// appleFeed is shaped like an iCloud published calendar: CRLF endings, a
// VTIMEZONE block Apple always emits, X-APPLE-* and X-WR-* properties, a
// VALARM inside the event, and a timed event whose zone is given by TZID
// rather than by a Z suffix.
const appleFeed = "BEGIN:VCALENDAR\r\n" +
	"VERSION:2.0\r\n" +
	"PRODID:-//Apple Inc.//macOS 15.3//EN\r\n" +
	"CALSCALE:GREGORIAN\r\n" +
	"X-WR-CALNAME:Family\r\n" +
	"X-WR-TIMEZONE:America/Chicago\r\n" +
	"X-APPLE-CALENDAR-COLOR:#FF2968\r\n" +
	"BEGIN:VTIMEZONE\r\n" +
	"TZID:America/Chicago\r\n" +
	"BEGIN:DAYLIGHT\r\n" +
	"TZOFFSETFROM:-0600\r\n" +
	"RRULE:FREQ=YEARLY;BYMONTH=3;BYDAY=2SU\r\n" +
	"DTSTART:20070311T020000\r\n" +
	"TZNAME:CDT\r\n" +
	"TZOFFSETTO:-0500\r\n" +
	"END:DAYLIGHT\r\n" +
	"BEGIN:STANDARD\r\n" +
	"TZOFFSETFROM:-0500\r\n" +
	"RRULE:FREQ=YEARLY;BYMONTH=11;BYDAY=1SU\r\n" +
	"DTSTART:20071104T020000\r\n" +
	"TZNAME:CST\r\n" +
	"TZOFFSETTO:-0600\r\n" +
	"END:STANDARD\r\n" +
	"END:VTIMEZONE\r\n" +
	"BEGIN:VEVENT\r\n" +
	"CREATED:20250501T120000Z\r\n" +
	"UID:5F1A9E20-1C4B-4E6E-9E4C-3B1D0A2F7C88\r\n" +
	"DTEND;TZID=America/Chicago:20250610T110000\r\n" +
	"TRANSP:OPAQUE\r\n" +
	"X-APPLE-TRAVEL-ADVISORY-BEHAVIOR:AUTOMATIC\r\n" +
	"SUMMARY:Swim lesson\r\n" +
	"LOCATION:Rec Center\\, 400 Main St\r\n" +
	"DTSTAMP:20250501T120000Z\r\n" +
	"DTSTART;TZID=America/Chicago:20250610T100000\r\n" +
	"SEQUENCE:0\r\n" +
	"X-APPLE-CREATOR-IDENTITY:com.apple.mobilecal\r\n" +
	"BEGIN:VALARM\r\n" +
	"X-WR-ALARMUID:9C7F1B2E-0000-0000-0000-000000000001\r\n" +
	"UID:9C7F1B2E-0000-0000-0000-000000000001\r\n" +
	"TRIGGER:-PT15M\r\n" +
	"DESCRIPTION:Event reminder\r\n" +
	"ACTION:DISPLAY\r\n" +
	"END:VALARM\r\n" +
	"END:VEVENT\r\n" +
	"BEGIN:VEVENT\r\n" +
	"UID:7D2B4C10-9A3E-4F55-8B21-6E0C5D9A1F33\r\n" +
	"DTSTART;VALUE=DATE:20250704\r\n" +
	"DTEND;VALUE=DATE:20250705\r\n" +
	"SUMMARY:Trip to the lake\r\n" +
	"X-APPLE-EWS-BUSYSTATUS:FREE\r\n" +
	"END:VEVENT\r\n" +
	"BEGIN:VEVENT\r\n" +
	"UID:2E8F6A44-5C1D-4B90-A7E3-8F4D2C6B0A15\r\n" +
	"DTSTART;TZID=America/Chicago:20250602T190000\r\n" +
	"DTEND;TZID=America/Chicago:20250602T200000\r\n" +
	"RRULE:FREQ=WEEKLY;BYDAY=MO\r\n" +
	"SUMMARY:Book club\r\n" +
	"END:VEVENT\r\n" +
	"END:VCALENDAR\r\n"

// An iCloud feed must parse with its zones resolved. The device carries no
// /usr/share/zoneinfo, so this also covers the embedded tzdata: without it
// TZID=America/Chicago would silently fall back and every Apple event would
// sit at the wrong hour.
func TestParseAppleFeed(t *testing.T) {
	res := parse(t, appleFeed, Options{
		From: mustTime(t, "2025-06-01T00:00:00Z"),
		To:   mustTime(t, "2025-07-10T00:00:00Z"),
		Loc:  time.UTC,
	})

	byTitle := map[string]Event{}
	counts := map[string]int{}
	for _, e := range res.Events {
		byTitle[e.Summary] = e
		counts[e.Summary]++
	}

	swim, ok := byTitle["Swim lesson"]
	if !ok {
		t.Fatalf("timed event missing; got %v", counts)
	}
	// 10:00 Chicago in June is CDT, UTC-5.
	if want := mustTime(t, "2025-06-10T15:00:00Z"); !swim.Start.Equal(want) {
		t.Errorf("Start = %v, want %v — TZID was not resolved", swim.Start.UTC(), want)
	}
	if swim.Duration() != time.Hour {
		t.Errorf("Duration = %v, want 1h", swim.Duration())
	}
	if swim.AllDay {
		t.Error("timed event reported as all-day")
	}
	if swim.Location != "Rec Center, 400 Main St" {
		t.Errorf("Location = %q — escaping not undone", swim.Location)
	}

	trip, ok := byTitle["Trip to the lake"]
	if !ok {
		t.Fatal("all-day event missing")
	}
	if !trip.AllDay {
		t.Error("VALUE=DATE event not reported as all-day")
	}

	// Mondays 2, 9, 16, 23, 30 June and 7 July.
	if counts["Book club"] != 6 {
		t.Errorf("recurring event expanded %d times, want 6", counts["Book club"])
	}
}

// Apple's share sheet hands out webcal:// links. A user pastes what they were
// given, so the feed must work without them knowing to edit the scheme.
func TestAppleWebcalURLIsAccepted(t *testing.T) {
	const raw = "webcal://p37-calendars.icloud.com/published/2/MTQ4NTk4NDE2OTE0ODU5OA"
	got := NormalizeURL(raw)
	if want := "https://p37-calendars.icloud.com/published/2/MTQ4NTk4NDE2OTE0ODU5OA"; got != want {
		t.Errorf("NormalizeURL(%q) = %q, want %q", raw, got, want)
	}
}

func TestNormalizeURL(t *testing.T) {
	cases := map[string]string{
		"webcal://example.com/a.ics":   "https://example.com/a.ics",
		"WEBCAL://example.com/a.ics":   "https://example.com/a.ics",
		"webcals://example.com/a.ics":  "https://example.com/a.ics",
		"https://example.com/a.ics":    "https://example.com/a.ics",
		"http://example.com/a.ics":     "http://example.com/a.ics",
		"  webcal://example.com/a.ics": "https://example.com/a.ics",

		// Bare hosts: a user pasting from a share sheet may lose the scheme.
		"p37-calendars.icloud.com/published/2/X": "https://p37-calendars.icloud.com/published/2/X",

		// Left alone: not ours to guess at.
		"":         "",
		"file:///etc/passwd": "file:///etc/passwd",
	}
	for in, want := range cases {
		if got := NormalizeURL(in); got != want {
			t.Errorf("NormalizeURL(%q) = %q, want %q", in, got, want)
		}
	}
}
