package source

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const miniFeed = "BEGIN:VCALENDAR\r\n" +
	"VERSION:2.0\r\n" +
	"PRODID:-//Apple Inc.//macOS 15.3//EN\r\n" +
	"BEGIN:VEVENT\r\n" +
	"UID:5F1A9E20\r\n" +
	"SUMMARY:Swim lesson\r\n" +
	"DTSTART;TZID=America/Chicago:20250610T100000\r\n" +
	"DTEND;TZID=America/Chicago:20250610T110000\r\n" +
	"END:VEVENT\r\n" +
	"END:VCALENDAR\r\n"

// Apple's share sheet gives out webcal:// links, and a user pastes what they
// were given. Go's http client rejects the scheme outright, so without
// normalisation an iCloud calendar fails with "unsupported protocol scheme"
// — an error that tells the user nothing about what to do.
func TestFetchAcceptsWebcalScheme(t *testing.T) {
	var gotAccept string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAccept = r.Header.Get("Accept")
		w.Header().Set("Content-Type", "text/calendar; charset=utf-8")
		w.Write([]byte(miniFeed))
	}))
	defer srv.Close()

	// https://host → webcal://host, as the share sheet would have written it.
	webcal := "webcal://" + strings.TrimPrefix(srv.URL, "https://") + "/published/2/TOKEN"

	c := NewCalendar([]Feed{{ID: "family", Name: "Family", URL: webcal, Color: "#FF2968"}},
		time.UTC, time.Minute)
	c.client = srv.Client()
	c.now = func() time.Time { return time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC) }

	data, err := c.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	cal, ok := data.(CalendarData)
	if !ok {
		t.Fatalf("Fetch returned %T", data)
	}
	if len(cal.FeedErrors) != 0 {
		t.Fatalf("feed errors: %v", cal.FeedErrors)
	}
	if len(cal.Events) != 1 {
		t.Fatalf("got %d events, want 1", len(cal.Events))
	}
	if cal.Events[0].Summary != "Swim lesson" {
		t.Errorf("Summary = %q", cal.Events[0].Summary)
	}
	if cal.Events[0].Color != "#FF2968" {
		t.Errorf("feed colour not stamped: %q", cal.Events[0].Color)
	}
	if gotAccept != "text/calendar" {
		t.Errorf("Accept = %q", gotAccept)
	}
}

// A failing feed must name its host and nothing else: the error reaches the
// log and the status page.
func TestFetchErrorDoesNotLeakTheLink(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusForbidden)
	}))
	defer srv.Close()

	webcal := "webcal://" + strings.TrimPrefix(srv.URL, "https://") + "/published/2/SECRETTOKEN"

	c := NewCalendar([]Feed{{ID: "family", URL: webcal}}, time.UTC, time.Minute)
	c.client = srv.Client()

	_, err := c.Fetch(context.Background())
	if err == nil {
		t.Fatal("expected an error when every feed fails")
	}
	if strings.Contains(err.Error(), "SECRETTOKEN") {
		t.Errorf("error leaked the calendar link: %v", err)
	}
	if !strings.Contains(err.Error(), "403") {
		t.Errorf("error does not say what went wrong: %v", err)
	}
}
