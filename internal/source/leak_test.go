package source

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// A calendar subscription URL is a credential: anyone holding it can read the
// calendar. This mirror writes its errors to mm.log on the SD card, serves
// them from /api/logs to anyone on the network, and shows them on the status
// page — and it hangs on a wall in a house that is not the owner's.
//
// The redaction existed and was applied to exactly one of the four paths an
// error can take out of a feed fetch. A device that could not resolve DNS
// logged the complete Google link, token and all.

const secretToken = "private-11b70c261cfa95f0e3730f62712d5691"

func secretFeedURL(host string) string {
	return host + "/calendar/ical/somebody%40group.calendar.google.com/" +
		secretToken + "/basic.ics"
}

// mustNotLeak fails if the token appears anywhere in the error text.
func mustNotLeak(t *testing.T, err error, path string) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s: expected an error", path)
	}
	if strings.Contains(err.Error(), secretToken) {
		t.Errorf("%s leaked the subscription token:\n  %v", path, err)
	}
}

// The path that leaked in the field: DNS failure, connection refused, TLS
// failure — anything the transport reports is wrapped in *url.Error, which
// prints the whole request URL.
func TestTransportErrorDoesNotLeakTheToken(t *testing.T) {
	c := NewCalendar([]Feed{{
		ID:  "family",
		URL: secretFeedURL("https://this-host-does-not-resolve.invalid"),
	}}, time.UTC, time.Minute)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	_, err := c.Fetch(ctx)
	mustNotLeak(t, err, "transport error")
}

// A feed that answers with an error status.
func TestHTTPStatusErrorDoesNotLeakTheToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "gone", http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)

	c := NewCalendar([]Feed{{ID: "family", URL: secretFeedURL(srv.URL)}}, time.UTC, time.Minute)

	_, err := c.Fetch(context.Background())
	mustNotLeak(t, err, "http status error")
}

// A feed that answers with something that is not a calendar.
func TestParseErrorDoesNotLeakTheToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("<html>this is a captive portal, not a calendar</html>"))
	}))
	t.Cleanup(srv.Close)

	c := NewCalendar([]Feed{{ID: "family", URL: secretFeedURL(srv.URL)}}, time.UTC, time.Minute)

	_, err := c.Fetch(context.Background())
	mustNotLeak(t, err, "parse error")
}

// A URL that cannot be parsed into a request at all.
func TestMalformedURLDoesNotLeakTheToken(t *testing.T) {
	c := NewCalendar([]Feed{{
		ID: "family",
		// A control character makes NewRequest fail, and it reports
		// parse "<the whole url>": …
		URL: "https://calendar.example.invalid/\x7f/" + secretToken + "/basic.ics",
	}}, time.UTC, time.Minute)

	_, err := c.Fetch(context.Background())
	mustNotLeak(t, err, "malformed url")
}

// The redaction has to leave something useful behind. Knowing which provider
// is failing is the difference between a diagnosis and a shrug, and the feed
// is already identified by name alongside.
func TestRedactionKeepsTheHost(t *testing.T) {
	c := NewCalendar([]Feed{{
		ID:  "family",
		URL: secretFeedURL("https://calendar.google.com.this-does-not-resolve.invalid"),
	}}, time.UTC, time.Minute)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	_, err := c.Fetch(ctx)
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "this-does-not-resolve.invalid") {
		t.Errorf("redaction removed the host as well as the secret: %v", err)
	}
}

// redactErr is used on several paths; check the shapes directly too.
func TestRedactErrShapes(t *testing.T) {
	raw := secretFeedURL("https://calendar.google.com")

	t.Run("nil stays nil", func(t *testing.T) {
		if redactErr(nil, raw) != nil {
			t.Error("turned a nil error into something")
		}
	})

	t.Run("unrelated errors are untouched", func(t *testing.T) {
		in := errors.New("the disk caught fire")
		if got := redactErr(in, raw); got.Error() != in.Error() {
			t.Errorf("rewrote an error that never mentioned the url: %v", got)
		}
	})

	t.Run("an embedded url is replaced", func(t *testing.T) {
		in := &wrapped{msg: "reading " + raw + " failed"}
		got := redactErr(in, raw)
		if strings.Contains(got.Error(), secretToken) {
			t.Errorf("token survived: %v", got)
		}
	})
}

type wrapped struct{ msg string }

func (w *wrapped) Error() string { return w.msg }
