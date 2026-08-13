// Package ics turns iCalendar feeds into the occurrences a mirror needs.
//
// Parsing and recurrence are delegated to libraries rather than hand-rolled:
//
//   - github.com/arran4/golang-ical for the wire format — line folding,
//     parameters, VTIMEZONE, property escaping.
//   - github.com/teambition/rrule-go for recurrence, a port of
//     python-dateutil's rrule that implements the full RFC 5545 rule set.
//
// An earlier version of this file did both by hand. It was fine for the
// common cases and quietly wrong for several real ones — BYSETPOS, RDATE,
// and most importantly RECURRENCE-ID, which is how every calendar app
// represents "I moved one occurrence of this meeting". Without handling it,
// a moved event appears twice: once at its original slot from the rule, and
// once at its new time.
//
// What remains here is the part no library can decide: which occurrences a
// mirror should show, and how overrides and cancellations resolve.
package ics

import (
	"bytes"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	goics "github.com/arran4/golang-ical"
	"github.com/teambition/rrule-go"
)

// Event is one occurrence. A recurring VEVENT expands into several.
type Event struct {
	UID      string
	Summary  string
	Location string
	Start    time.Time
	End      time.Time
	AllDay   bool

	// FeedID and Color are stamped by the caller so widgets can colour
	// events by which calendar they came from.
	FeedID string
	Color  string
}

// Duration is how long the event lasts.
func (e Event) Duration() time.Duration { return e.End.Sub(e.Start) }

// Options bound what Parse returns.
type Options struct {
	// From and To bound the window. Occurrences outside it are dropped,
	// which is also what stops an unbounded RRULE from expanding forever.
	From time.Time
	To   time.Time

	// Loc interprets floating (timezone-less) times. Defaults to UTC.
	Loc *time.Location

	// Max caps the number of events returned. Zero means unlimited.
	//
	// v1 used fixed arrays and silently dropped the overflow. Here the cap
	// is explicit and reported.
	Max int
}

// Result is what a parse produced.
type Result struct {
	Events []Event

	// Truncated reports that Max was hit and events were dropped.
	Truncated bool

	// Skipped counts VEVENTs that could not be parsed. A malformed event
	// should cost you that event, not the whole feed.
	Skipped int
}

// occurrenceCap bounds what a single series contributes, so one pathological
// rule cannot exhaust memory even inside a wide window.
const occurrenceCap = 2000

// occurrenceScanCap bounds how far a series is walked looking for occurrences
// inside the window.
//
// The two are different limits. A rule can start years before the window and
// still be legitimate — a birthday recurring since the 1970s — so reaching the
// window means stepping through everything before it. Without this, a
// second-by-second rule with an old DTSTART burns a core walking to the
// present, on a device that has exactly one.
//
// Generous next to anything real: a daily event started at the millennium
// needs around nine thousand steps.
const occurrenceScanCap = 200_000

// maxFeed bounds how much of a feed is read. A misconfigured URL pointing at
// something enormous should fail cleanly rather than exhaust a 512MB device.
const maxFeed = 16 << 20

// parseCalendar reads a feed, tolerating one that omits its VCALENDAR
// wrapper.
//
// Every real feed has one and the library rightly insists on it. But losing
// an entire calendar to a missing header is a poor trade for a device whose
// whole job is showing that calendar, so a bare stream of VEVENTs is wrapped
// and retried rather than discarded.
func parseCalendar(r io.Reader) (*goics.Calendar, error) {
	body, err := io.ReadAll(io.LimitReader(r, maxFeed))
	if err != nil {
		return nil, fmt.Errorf("read calendar: %w", err)
	}

	cal, err := goics.ParseCalendar(bytes.NewReader(body))
	if err == nil {
		return cal, nil
	}
	if !bytes.Contains(bytes.ToUpper(body), []byte("BEGIN:VEVENT")) {
		return nil, fmt.Errorf("parse calendar: %w", err)
	}

	wrapped := append([]byte("BEGIN:VCALENDAR\r\nVERSION:2.0\r\nPRODID:-//magic-mirror//EN\r\n"), body...)
	wrapped = append(wrapped, []byte("\r\nEND:VCALENDAR\r\n")...)

	cal, wrapErr := goics.ParseCalendar(bytes.NewReader(wrapped))
	if wrapErr != nil {
		return nil, fmt.Errorf("parse calendar: %w", err)
	}
	return cal, nil
}

// Parse reads an iCalendar stream.
func Parse(r io.Reader, opts Options) (Result, error) {
	if opts.Loc == nil {
		opts.Loc = time.UTC
	}

	cal, err := parseCalendar(r)
	if err != nil {
		return Result{}, err
	}

	var res Result

	// Overrides are collected first, because expanding a series needs to
	// know which of its occurrences have been moved or cancelled.
	overrides, cancelled := collectOverrides(cal, opts.Loc)

	for _, ev := range cal.Events() {
		if recurrenceID(ev, opts.Loc) != nil {
			continue // handled as an override
		}
		events, err := expand(ev, opts, overrides, cancelled)
		if err != nil {
			res.Skipped++
			continue
		}
		res.Events = append(res.Events, events...)
	}

	// Overrides that moved an occurrence contribute their new time.
	for _, o := range overrides {
		if o.event == nil {
			continue
		}
		if overlaps(o.event.Start, o.event.End, opts.From, opts.To) {
			res.Events = append(res.Events, *o.event)
		}
	}

	sort.Slice(res.Events, func(i, j int) bool {
		if res.Events[i].Start.Equal(res.Events[j].Start) {
			return res.Events[i].Summary < res.Events[j].Summary
		}
		return res.Events[i].Start.Before(res.Events[j].Start)
	})

	if opts.Max > 0 && len(res.Events) > opts.Max {
		res.Events = res.Events[:opts.Max]
		res.Truncated = true
	}
	return res, nil
}

// override is a single modified occurrence of a series.
type override struct {
	event *Event // nil when the occurrence was cancelled outright
}

// overrideKey identifies one occurrence of one series.
type overrideKey struct {
	uid  string
	when int64
}

// collectOverrides finds every VEVENT carrying a RECURRENCE-ID.
//
// These are how calendar apps express "this one occurrence is different":
// a separate VEVENT sharing the series UID, naming the occurrence it
// replaces. The original must be suppressed or the event shows twice.
func collectOverrides(cal *goics.Calendar, loc *time.Location) (map[overrideKey]*override, map[overrideKey]bool) {
	out := map[overrideKey]*override{}
	cancelled := map[overrideKey]bool{}

	for _, ev := range cal.Events() {
		rid := recurrenceID(ev, loc)
		if rid == nil {
			continue
		}
		uid := propValue(ev, string(goics.ComponentPropertyUniqueId))
		key := overrideKey{uid: uid, when: rid.Unix()}

		if isCancelled(ev) {
			cancelled[key] = true
			out[key] = &override{}
			continue
		}

		e, err := single(ev, loc)
		if err != nil {
			cancelled[key] = true // suppress the original; we cannot render this
			out[key] = &override{}
			continue
		}
		out[key] = &override{event: &e}
	}
	return out, cancelled
}

// expand turns one VEVENT into the occurrences inside the window.
func expand(ev *goics.VEvent, opts Options, overrides map[overrideKey]*override, cancelled map[overrideKey]bool) ([]Event, error) {
	if isCancelled(ev) {
		return nil, nil
	}

	base, err := single(ev, opts.Loc)
	if err != nil {
		return nil, err
	}

	rule := propValue(ev, string(goics.ComponentPropertyRrule))
	if rule == "" {
		if overlaps(base.Start, base.End, opts.From, opts.To) {
			return []Event{base}, nil
		}
		return nil, nil
	}

	set, err := buildSet(ev, base.Start, rule, opts.Loc)
	if err != nil {
		return nil, err
	}

	// Between is inclusive and bounded by the window, which is what keeps
	// an unbounded rule from expanding forever.
	from, to := opts.From, opts.To
	if from.IsZero() {
		from = base.Start
	}
	if to.IsZero() {
		return nil, fmt.Errorf("recurring event %q needs a bounded window", base.UID)
	}

	dur := base.Duration()
	lower := from.Add(-dur)

	// Walk the series rather than materialising it.
	//
	// set.Between builds the entire slice before returning, and a recurrence
	// rule is allowed to be pathological: FREQ=SECONDLY is valid RFC 5545, and
	// across the ninety day window this device uses that is nearly eight
	// million occurrences — around 190MB of time.Time, followed by a result
	// slice preallocated to match, on a machine with 512MB and no swap. The
	// occurrence cap was applied after both allocations, so it bounded the
	// output and nothing else.
	//
	// The device is killed by the kernel long before it renders anything, and
	// the restart that follows counts as a failure, so three of them roll the
	// mirror back to a build with the same feed still configured. Nothing
	// about that is recoverable from the house it is hanging in.
	//
	// So the caps apply while walking: one on what is produced, and one on how
	// far the walk goes, because a rule starting years before the window still
	// has to be stepped through to reach it.
	out := make([]Event, 0, 64)
	next := set.Iterator()
	for scanned := 0; scanned < occurrenceScanCap; scanned++ {
		s, ok := next()
		if !ok || s.After(to) {
			break
		}
		if s.Before(lower) {
			continue
		}
		if len(out) >= occurrenceCap {
			break
		}

		key := overrideKey{uid: base.UID, when: s.Unix()}
		// A moved or cancelled occurrence is not drawn here: the override
		// contributes its own time, or nothing at all.
		if _, moved := overrides[key]; moved {
			continue
		}
		if cancelled[key] {
			continue
		}

		e := base
		e.Start = s
		e.End = s.Add(dur)
		if overlaps(e.Start, e.End, opts.From, opts.To) {
			out = append(out, e)
		}
	}
	return out, nil
}

// buildSet assembles the recurrence rule with its RDATEs and EXDATEs.
func buildSet(ev *goics.VEvent, start time.Time, rule string, loc *time.Location) (*rrule.Set, error) {
	opt, err := rrule.StrToROption(rule)
	if err != nil {
		return nil, fmt.Errorf("bad RRULE %q: %w", rule, err)
	}
	opt.Dtstart = start

	r, err := rrule.NewRRule(*opt)
	if err != nil {
		return nil, fmt.Errorf("build RRULE %q: %w", rule, err)
	}

	set := &rrule.Set{}
	set.DTStart(start)
	set.RRule(r)

	for _, t := range dateList(ev, "EXDATE", loc) {
		set.ExDate(t)
	}
	for _, t := range dateList(ev, "RDATE", loc) {
		set.RDate(t)
	}
	return set, nil
}

// single builds a non-recurring Event from a VEVENT.
func single(ev *goics.VEvent, loc *time.Location) (Event, error) {
	uid := propValue(ev, string(goics.ComponentPropertyUniqueId))

	start, allDay, ok := eventTime(ev, string(goics.ComponentPropertyDtStart), loc)
	if !ok {
		return Event{}, fmt.Errorf("event %q has no usable DTSTART", uid)
	}

	end, _, hasEnd := eventTime(ev, string(goics.ComponentPropertyDtEnd), loc)
	if !hasEnd {
		if d := propValue(ev, "DURATION"); d != "" {
			if dur, err := parseISODuration(d); err == nil {
				end = start.Add(dur)
				hasEnd = true
			}
		}
	}
	if !hasEnd {
		// RFC 5545: a DATE-valued DTSTART with no end lasts one day;
		// anything else defaults to an hour, which is what calendar apps do.
		if allDay {
			end = start.AddDate(0, 0, 1)
		} else {
			end = start.Add(time.Hour)
		}
	}
	if end.Before(start) {
		end = start
	}

	return Event{
		UID:      uid,
		Summary:  propValue(ev, string(goics.ComponentPropertySummary)),
		Location: propValue(ev, string(goics.ComponentPropertyLocation)),
		Start:    start,
		End:      end,
		AllDay:   allDay,
	}, nil
}

// eventTime reads a date-time property, reporting whether it is a whole-day
// value.
func eventTime(ev *goics.VEvent, name string, loc *time.Location) (time.Time, bool, bool) {
	p := ev.GetProperty(goics.ComponentProperty(name))
	if p == nil || p.Value == "" {
		return time.Time{}, false, false
	}
	return parseValue(p.Value, paramValue(p, "VALUE"), paramValue(p, "TZID"), loc)
}

// recurrenceID returns the occurrence a VEVENT overrides, or nil.
func recurrenceID(ev *goics.VEvent, loc *time.Location) *time.Time {
	p := ev.GetProperty(goics.ComponentProperty("RECURRENCE-ID"))
	if p == nil || p.Value == "" {
		return nil
	}
	t, _, ok := parseValue(p.Value, paramValue(p, "VALUE"), paramValue(p, "TZID"), loc)
	if !ok {
		return nil
	}
	return &t
}

// dateList reads every value of a possibly-repeated date property.
//
// EXDATE and RDATE may appear on several lines and each line may hold a
// comma-separated list, so both have to be walked.
func dateList(ev *goics.VEvent, name string, loc *time.Location) []time.Time {
	var out []time.Time
	for _, p := range ev.Properties {
		if !strings.EqualFold(p.IANAToken, name) {
			continue
		}
		for _, part := range strings.Split(p.Value, ",") {
			if t, _, ok := parseValue(strings.TrimSpace(part),
				paramValue(&p, "VALUE"), paramValue(&p, "TZID"), loc); ok {
				out = append(out, t)
			}
		}
	}
	return out
}

// parseValue handles the three forms a date-time property takes: UTC with a
// Z suffix, a local time with a TZID, and a floating time with neither.
func parseValue(v, valueType, tzid string, def *time.Location) (time.Time, bool, bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return time.Time{}, false, false
	}

	zone := def
	if tzid != "" {
		if l, err := time.LoadLocation(tzid); err == nil {
			zone = l
		}
		// An unknown TZID falls back rather than failing: an event at a
		// plausible hour beats losing the whole feed to one exotic zone.
	}

	if strings.EqualFold(valueType, "DATE") || len(v) == 8 {
		t, err := time.ParseInLocation("20060102", v, zone)
		return t, true, err == nil
	}
	if strings.HasSuffix(v, "Z") {
		t, err := time.ParseInLocation("20060102T150405Z", v, time.UTC)
		return t.UTC(), false, err == nil
	}
	t, err := time.ParseInLocation("20060102T150405", v, zone)
	return t, false, err == nil
}

func propValue(ev *goics.VEvent, name string) string {
	if p := ev.GetProperty(goics.ComponentProperty(name)); p != nil {
		return p.Value
	}
	return ""
}

func paramValue(p *goics.IANAProperty, name string) string {
	if p == nil {
		return ""
	}
	if vs, ok := p.ICalParameters[name]; ok && len(vs) > 0 {
		return vs[0]
	}
	return ""
}

func isCancelled(ev *goics.VEvent) bool {
	return strings.EqualFold(propValue(ev, string(goics.ComponentPropertyStatus)), "CANCELLED")
}

func overlaps(aStart, aEnd, bStart, bEnd time.Time) bool {
	if !bStart.IsZero() && !aEnd.After(bStart) {
		return false
	}
	if !bEnd.IsZero() && !aStart.Before(bEnd) {
		return false
	}
	return true
}

// parseISODuration handles the ISO 8601 subset iCalendar uses, e.g. P1DT2H30M.
func parseISODuration(s string) (time.Duration, error) {
	s = strings.TrimSpace(strings.ToUpper(s))
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimLeft(s, "+-")
	if !strings.HasPrefix(s, "P") {
		return 0, fmt.Errorf("not a duration: %q", s)
	}
	s = s[1:]

	var total time.Duration
	inTime := false
	num := 0
	haveNum := false

	for _, r := range s {
		switch {
		case r == 'T':
			inTime = true
			num, haveNum = 0, false
		case r >= '0' && r <= '9':
			num = num*10 + int(r-'0')
			haveNum = true
		default:
			if !haveNum {
				continue
			}
			switch r {
			case 'W':
				total += time.Duration(num) * 7 * 24 * time.Hour
			case 'D':
				total += time.Duration(num) * 24 * time.Hour
			case 'H':
				if inTime {
					total += time.Duration(num) * time.Hour
				}
			case 'M':
				if inTime {
					total += time.Duration(num) * time.Minute
				}
			case 'S':
				total += time.Duration(num) * time.Second
			}
			num, haveNum = 0, false
		}
	}
	if neg {
		return -total, nil
	}
	return total, nil
}
