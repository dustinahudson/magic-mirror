package source

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/dustinahudson/magic-mirror/internal/ics"
)

// KeyCalendar is the store key calendar data is published under.
const KeyCalendar = "calendar"

// Feed identifies one calendar to fetch.
type Feed struct {
	ID    string
	URL   string
	Name  string
	Color string
}

// CalendarData is the aggregated result of fetching every feed.
type CalendarData struct {
	Events []ics.Event

	// FeedErrors records which feeds failed, keyed by feed ID.
	//
	// Partial failure is the common case — one calendar 404s while the rest
	// are fine — and it must not discard the feeds that worked. v1 treated
	// "zero events total" as the only failure signal (kernel.cpp:519), which
	// could not distinguish "one feed is broken" from "this week is empty".
	FeedErrors map[string]string

	// Truncated reports that the event cap was hit.
	Truncated bool
}

// OK reports whether every feed fetched cleanly.
func (d CalendarData) OK() bool { return len(d.FeedErrors) == 0 }

// CalendarSource fetches and parses ICS feeds.
type CalendarSource struct {
	Feeds     []Feed
	Window    time.Duration
	Interval_ time.Duration
	Max       int
	Loc       *time.Location

	client *http.Client
	now    func() time.Time
}

// NewCalendar returns a calendar fetcher.
//
// The window matches v1's three-month horizon (kernel.cpp:485), which is
// what the month grid needs to show adjacent months without a refetch.
func NewCalendar(feeds []Feed, loc *time.Location, interval time.Duration) *CalendarSource {
	if interval <= 0 {
		interval = 5 * time.Minute
	}
	if loc == nil {
		loc = time.UTC
	}
	return &CalendarSource{
		Feeds:     feeds,
		Window:    90 * 24 * time.Hour,
		Interval_: interval,
		Max:       500,
		Loc:       loc,
		// Generous: an ICS feed can be hundreds of kilobytes, and the
		// deadline that matters is the one the Manager imposes.
		client: HTTPClient(60 * time.Second),
		now:    time.Now,
	}
}

func (c *CalendarSource) Key() string             { return KeyCalendar }
func (c *CalendarSource) Interval() time.Duration { return c.Interval_ }

// NeedsNetwork reports that every feed is an HTTP fetch.
func (c *CalendarSource) NeedsNetwork() bool { return true }
func (c *CalendarSource) Timeout() time.Duration {
	// Scale with feed count, since they are fetched in sequence, but keep a
	// hard ceiling. This is the deadline that turns a dead calendar server
	// into an error instead of the indefinite hang that rebooted v1.
	return min(90*time.Second, 20*time.Second+time.Duration(len(c.Feeds))*20*time.Second)
}

func (c *CalendarSource) Fetch(ctx context.Context) (any, error) {
	if len(c.Feeds) == 0 {
		return CalendarData{FeedErrors: map[string]string{}}, nil
	}

	now := c.now()
	opts := ics.Options{
		From: now.Add(-24 * time.Hour), // yesterday, so today's events survive
		To:   now.Add(c.Window),
		Loc:  c.Loc,
		Max:  c.Max,
	}

	out := CalendarData{FeedErrors: map[string]string{}}

	// Sequential rather than concurrent: a Pi Zero W has one core and 512MB,
	// and parsing several large feeds at once buys latency we do not need at
	// a five-minute interval.
	for _, feed := range c.Feeds {
		if err := ctx.Err(); err != nil {
			out.FeedErrors[feed.ID] = "cancelled"
			continue
		}
		if strings.TrimSpace(feed.URL) == "" {
			continue
		}

		events, truncated, err := c.fetchFeed(ctx, feed, opts)
		if err != nil {
			out.FeedErrors[feed.ID] = err.Error()
			continue
		}
		out.Events = append(out.Events, events...)
		out.Truncated = out.Truncated || truncated
	}

	// Every feed failed: that is a source failure, so the store keeps the
	// previous events and marks them Stale rather than blanking the display.
	if len(out.Events) == 0 && len(out.FeedErrors) == len(c.Feeds) {
		return nil, fmt.Errorf("all %d calendar feeds failed: %s",
			len(c.Feeds), summarise(out.FeedErrors))
	}

	// Before sorting, while the events are still grouped by feed in the order
	// the feeds are configured — which is what decides who wins a duplicate.
	out.Events = dedupeAcrossFeeds(out.Events)

	sort.Slice(out.Events, func(i, j int) bool {
		return out.Events[i].Start.Before(out.Events[j].Start)
	})
	if c.Max > 0 && len(out.Events) > c.Max {
		out.Events = out.Events[:c.Max]
		out.Truncated = true
	}
	return out, nil
}

// dedupeAcrossFeeds drops an event that an earlier feed already supplied.
//
// Households share calendars. The same holiday, the same school closure, the
// same trip is invited to two people and lands in both their feeds, and the
// mirror drew it twice — two bars in a row, or the same title stacked in one
// day cell, which reads as two commitments rather than one.
//
// The winner is whichever feed comes first in the configuration, so the event
// keeps that calendar's colour. That makes the ordering on the Calendars tab
// meaningful: put the calendar whose colour you want to see at the top.
//
// Two ways of recognising the same event, because the two failure modes are
// different:
//
//   - Same UID at the same start. An event genuinely shared between accounts
//     carries the same UID, and this catches it even if someone renamed their
//     copy.
//   - Same title at the same start. Copied rather than shared events get a
//     fresh UID, so the UID says nothing, and the title is all that is left.
//
// The start time is part of both keys, and that is not optional. Every
// occurrence of a recurring event carries the *same* UID — the expansion
// copies the base event and moves only the times — so keying on UID alone
// would collapse a weekly standup into a single Monday.
//
// Only duplicates across different feeds are removed. Two identical entries
// inside one calendar are that calendar's business: they are far more likely
// to be two real bookings, and silently hiding one would be a mirror lying
// about what is in the feed it was given.
func dedupeAcrossFeeds(events []ics.Event) []ics.Event {
	if len(events) < 2 {
		return events
	}

	// key -> the feed that claimed it.
	claimed := make(map[string]string, len(events)*2)
	out := make([]ics.Event, 0, len(events))

	for _, e := range events {
		at := strconv.FormatInt(e.Start.Unix(), 10)
		title := strings.ToLower(strings.TrimSpace(e.Summary))

		keys := make([]string, 0, 2)
		if e.UID != "" {
			keys = append(keys, "u\x00"+e.UID+"\x00"+at)
		}
		if title != "" {
			keys = append(keys, "t\x00"+title+"\x00"+at+"\x00"+strconv.FormatBool(e.AllDay))
		}

		duplicate := false
		for _, k := range keys {
			if owner, ok := claimed[k]; ok && owner != e.FeedID {
				duplicate = true
				break
			}
		}
		if duplicate {
			continue
		}

		for _, k := range keys {
			if _, ok := claimed[k]; !ok {
				claimed[k] = e.FeedID
			}
		}
		out = append(out, e)
	}
	return out
}

func (c *CalendarSource) fetchFeed(ctx context.Context, feed Feed, opts ics.Options) ([]ics.Event, bool, error) {
	// Normalised at fetch rather than at save: a config written by hand, or
	// by an older build, gets the same treatment as one typed into the UI.
	url := ics.NormalizeURL(feed.URL)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, false, err
	}
	req.Header.Set("Accept", "text/calendar")
	req.Header.Set("User-Agent", "magic-mirror/2")

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, false, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, false, &ErrHTTPStatus{URL: redact(url), Status: resp.StatusCode}
	}

	// Cap the body. A misconfigured URL pointing at something enormous
	// should fail cleanly rather than exhausting memory on a 512MB device.
	const maxBody = 16 << 20
	body := io.LimitReader(resp.Body, maxBody)

	res, err := ics.Parse(body, opts)
	if err != nil {
		return nil, false, err
	}

	for i := range res.Events {
		res.Events[i].FeedID = feed.ID
		res.Events[i].Color = feed.Color
	}
	return res.Events, res.Truncated, nil
}

func summarise(errs map[string]string) string {
	parts := make([]string, 0, len(errs))
	for id, msg := range errs {
		parts = append(parts, id+": "+msg)
	}
	sort.Strings(parts)
	return strings.Join(parts, "; ")
}

// redact reduces a feed URL to its scheme and host before it reaches a log,
// an error message or the screen. A subscription link is a credential —
// anyone holding it can read the calendar — and a mirror is by definition a
// display other people can read.
//
// Host-only, because every provider puts the secret somewhere different and
// all of them put it in the path:
//
//	https://calendar.google.com/calendar/ical/…/private-<token>/basic.ics
//	https://p37-calendars.icloud.com/published/2/<token>
//	https://outlook.office365.com/owa/calendar/<token>/<token>/calendar.ics
//
// An earlier version stripped only the query string, on the belief that
// Google's secret address carried its token there. It does not, and neither
// does anyone else's, so that redaction protected nothing at all.
//
// The host survives because it is the diagnostic worth having — it says
// which provider is failing — and it is not the secret. Which feed is
// affected is already reported alongside, by name.
func redact(raw string) string {
	s := strings.TrimSpace(raw)

	scheme := ""
	if i := strings.Index(s, "://"); i >= 0 {
		scheme, s = s[:i+3], s[i+3:]
	}
	// Drop any userinfo along with the host's path, query and fragment.
	if i := strings.IndexByte(s, '@'); i >= 0 {
		s = s[i+1:]
	}
	host := s
	if i := strings.IndexAny(s, "/?#"); i >= 0 {
		host = s[:i]
	}
	if host == "" {
		return "(no url)"
	}
	if len(host) == len(s) {
		return scheme + host
	}
	return scheme + host + "/…"
}
