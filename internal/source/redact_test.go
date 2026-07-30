package source

import (
	"strings"
	"testing"
)

// The tokens below are the shapes real providers use. None of them may
// survive redaction: the output goes to the log, the status page and, when a
// feed is failing, the screen.
func TestRedactKeepsOnlyTheHost(t *testing.T) {
	cases := []struct {
		name, url, want, secret string
	}{{
		name:   "google secret address",
		url:    "https://calendar.google.com/calendar/ical/abc%40group.calendar.google.com/private-9f3c1d/basic.ics",
		want:   "https://calendar.google.com/…",
		secret: "private-9f3c1d",
	}, {
		name:   "icloud published calendar",
		url:    "https://p37-calendars.icloud.com/published/2/MTQ4NTk4NDE2OTE0",
		want:   "https://p37-calendars.icloud.com/…",
		secret: "MTQ4NTk4NDE2OTE0",
	}, {
		name:   "outlook published calendar",
		url:    "https://outlook.office365.com/owa/calendar/7f2a9c/1b4e8d/calendar.ics",
		want:   "https://outlook.office365.com/…",
		secret: "1b4e8d",
	}, {
		name:   "token in the query",
		url:    "https://example.com/cal.ics?key=s3cret",
		want:   "https://example.com/…",
		secret: "s3cret",
	}, {
		name:   "credentials in userinfo",
		url:    "https://user:hunter2@example.com/cal.ics",
		want:   "https://example.com/…",
		secret: "hunter2",
	}, {
		name: "bare host has nothing to hide",
		url:  "https://example.com",
		want: "https://example.com",
	}}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := redact(c.url)
			if got != c.want {
				t.Errorf("redact() = %q, want %q", got, c.want)
			}
			if c.secret != "" && strings.Contains(got, c.secret) {
				t.Errorf("redact() leaked %q: %q", c.secret, got)
			}
		})
	}
}

func TestRedactEmpty(t *testing.T) {
	if got := redact("   "); got != "(no url)" {
		t.Errorf("redact(blank) = %q", got)
	}
}
