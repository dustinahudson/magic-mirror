// Package ics parses iCalendar feeds.
//
// Scoped deliberately to what a mirror needs: VEVENTs inside a time window,
// with recurrence expanded. It is not a general iCalendar implementation and
// does not try to be — but it is a package with no I/O in it, which means the
// gnarly parts (folding, timezones, RRULE) are testable with a string
// literal instead of an SD card.
package ics

import (
	"bufio"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"
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
	// v1 used fixed arrays (kernel.cpp: `static mm::CalendarEvent
	// refreshEvents[200]`) and silently dropped the overflow. Here the cap
	// is explicit and reported.
	Max int
}

// Result is what a parse produced.
type Result struct {
	Events []Event

	// Truncated reports that Max was hit and events were dropped. The
	// caller surfaces this rather than pretending the list is complete.
	Truncated bool

	// Skipped counts VEVENTs that could not be parsed. A malformed event
	// should cost you that event, not the whole feed.
	Skipped int
}

// Parse reads an iCalendar stream.
//
// Streaming rather than slurping: a year of a busy Google Calendar is
// hundreds of kilobytes, and on a 512MB device with other work to do there
// is no reason to hold it all at once.
func Parse(r io.Reader, opts Options) (Result, error) {
	if opts.Loc == nil {
		opts.Loc = time.UTC
	}

	var res Result
	sc := bufio.NewScanner(r)
	// Some feeds carry long single-line properties (a base64 attachment, a
	// pathological description). Give the scanner room rather than failing
	// the whole feed on one oversized line.
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	var (
		cur     *vevent
		folded  strings.Builder
		hasLine bool
	)

	flush := func() {
		if !hasLine {
			return
		}
		line := folded.String()
		folded.Reset()
		hasLine = false

		name, params, value := splitProperty(line)
		switch strings.ToUpper(name) {
		case "BEGIN":
			if strings.EqualFold(value, "VEVENT") {
				cur = &vevent{}
			}
		case "END":
			if strings.EqualFold(value, "VEVENT") && cur != nil {
				evs, err := cur.expand(opts)
				if err != nil {
					res.Skipped++
				} else {
					res.Events = append(res.Events, evs...)
				}
				cur = nil
			}
		default:
			if cur != nil {
				cur.set(name, params, value, opts.Loc)
			}
		}
	}

	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")

		// RFC 5545 line folding: a leading space or tab continues the
		// previous line. Getting this wrong truncates every long SUMMARY,
		// which is most of them.
		if len(line) > 0 && (line[0] == ' ' || line[0] == '\t') {
			if hasLine {
				folded.WriteString(line[1:])
			}
			continue
		}

		flush()
		if line == "" {
			continue
		}
		folded.WriteString(line)
		hasLine = true
	}
	flush()

	if err := sc.Err(); err != nil {
		// Return what was parsed so far alongside the error: a feed that
		// dies halfway through should still contribute the events it did
		// deliver.
		return res, fmt.Errorf("read calendar: %w", err)
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

// vevent accumulates properties between BEGIN:VEVENT and END:VEVENT.
type vevent struct {
	uid      string
	summary  string
	location string
	status   string
	start    time.Time
	end      time.Time
	allDay   bool
	hasStart bool
	hasEnd   bool
	duration time.Duration
	rrule    string
	exdates  []time.Time
}

func (v *vevent) set(name string, params map[string]string, value string, loc *time.Location) {
	switch strings.ToUpper(name) {
	case "UID":
		v.uid = value
	case "SUMMARY":
		v.summary = unescape(value)
	case "LOCATION":
		v.location = unescape(value)
	case "STATUS":
		v.status = strings.ToUpper(value)
	case "DTSTART":
		if t, allDay, ok := parseTime(value, params, loc); ok {
			v.start, v.allDay, v.hasStart = t, allDay, true
		}
	case "DTEND":
		if t, _, ok := parseTime(value, params, loc); ok {
			v.end, v.hasEnd = t, true
		}
	case "DURATION":
		v.duration = parseDuration(value)
	case "RRULE":
		v.rrule = value
	case "EXDATE":
		for _, part := range strings.Split(value, ",") {
			if t, _, ok := parseTime(part, params, loc); ok {
				v.exdates = append(v.exdates, t)
			}
		}
	}
}

// expand turns a VEVENT into the occurrences that fall inside the window.
func (v *vevent) expand(opts Options) ([]Event, error) {
	if !v.hasStart {
		return nil, fmt.Errorf("event %q has no DTSTART", v.uid)
	}
	if v.status == "CANCELLED" {
		return nil, nil
	}

	dur := v.duration
	if dur == 0 {
		if v.hasEnd {
			dur = v.end.Sub(v.start)
		} else if v.allDay {
			dur = 24 * time.Hour
		} else {
			dur = time.Hour
		}
	}
	if dur < 0 {
		dur = 0
	}

	base := Event{
		UID:      v.uid,
		Summary:  v.summary,
		Location: v.location,
		AllDay:   v.allDay,
	}

	emit := func(start time.Time) Event {
		e := base
		e.Start = start
		e.End = start.Add(dur)
		return e
	}

	if v.rrule == "" {
		if overlaps(v.start, v.start.Add(dur), opts.From, opts.To) {
			return []Event{emit(v.start)}, nil
		}
		return nil, nil
	}

	starts, err := expandRRULE(v.rrule, v.start, opts.From, opts.To)
	if err != nil {
		return nil, err
	}

	excluded := make(map[int64]bool, len(v.exdates))
	for _, ex := range v.exdates {
		excluded[ex.Unix()] = true
	}

	out := make([]Event, 0, len(starts))
	for _, s := range starts {
		if excluded[s.Unix()] {
			continue
		}
		if overlaps(s, s.Add(dur), opts.From, opts.To) {
			out = append(out, emit(s))
		}
	}
	return out, nil
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

// splitProperty breaks "NAME;PARAM=VAL:value" into its parts.
func splitProperty(line string) (name string, params map[string]string, value string) {
	params = map[string]string{}

	// The colon that ends the property name can be preceded by a quoted
	// parameter value containing its own colon, so track quoting.
	inQuote := false
	colon := -1
	for i := 0; i < len(line); i++ {
		switch line[i] {
		case '"':
			inQuote = !inQuote
		case ':':
			if !inQuote {
				colon = i
			}
		}
		if colon >= 0 {
			break
		}
	}
	if colon < 0 {
		return line, params, ""
	}

	head, value := line[:colon], line[colon+1:]

	parts := splitUnquoted(head, ';')
	name = parts[0]
	for _, p := range parts[1:] {
		k, v, found := strings.Cut(p, "=")
		if !found {
			continue
		}
		params[strings.ToUpper(k)] = strings.Trim(v, `"`)
	}
	return name, params, value
}

func splitUnquoted(s string, sep byte) []string {
	var out []string
	inQuote := false
	start := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '"':
			inQuote = !inQuote
		case sep:
			if !inQuote {
				out = append(out, s[start:i])
				start = i + 1
			}
		}
	}
	return append(out, s[start:])
}

// parseTime handles the three DTSTART forms: UTC with a Z suffix, a local
// time with a TZID parameter, and a floating time with neither.
func parseTime(value string, params map[string]string, def *time.Location) (time.Time, bool, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, false, false
	}

	if strings.EqualFold(params["VALUE"], "DATE") || len(value) == 8 {
		t, err := time.ParseInLocation("20060102", value, zoneFor(params, def))
		if err != nil {
			return time.Time{}, false, false
		}
		return t, true, true
	}

	if strings.HasSuffix(value, "Z") {
		t, err := time.ParseInLocation("20060102T150405Z", value, time.UTC)
		if err != nil {
			return time.Time{}, false, false
		}
		return t.UTC(), false, true
	}

	t, err := time.ParseInLocation("20060102T150405", value, zoneFor(params, def))
	if err != nil {
		return time.Time{}, false, false
	}
	return t, false, true
}

// zoneFor resolves a TZID parameter, falling back to the caller's default.
//
// An unknown TZID falls back rather than failing: an event at a plausible
// hour beats no event at all, and the alternative is a whole feed lost to
// one exotic zone name.
func zoneFor(params map[string]string, def *time.Location) *time.Location {
	tzid := params["TZID"]
	if tzid == "" {
		return def
	}
	if loc, err := time.LoadLocation(tzid); err == nil {
		return loc
	}
	return def
}

// parseDuration handles the ISO 8601 subset iCalendar uses, e.g. P1DT2H30M.
func parseDuration(s string) time.Duration {
	s = strings.TrimSpace(strings.ToUpper(s))
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimLeft(s, "+-")
	if !strings.HasPrefix(s, "P") {
		return 0
	}
	s = s[1:]

	var total time.Duration
	inTime := false
	num := ""

	for _, r := range s {
		switch {
		case r == 'T':
			inTime = true
			num = ""
		case r >= '0' && r <= '9':
			num += string(r)
		default:
			n, err := strconv.Atoi(num)
			num = ""
			if err != nil {
				continue
			}
			switch r {
			case 'W':
				total += time.Duration(n) * 7 * 24 * time.Hour
			case 'D':
				total += time.Duration(n) * 24 * time.Hour
			case 'H':
				if inTime {
					total += time.Duration(n) * time.Hour
				}
			case 'M':
				if inTime {
					total += time.Duration(n) * time.Minute
				}
			case 'S':
				total += time.Duration(n) * time.Second
			}
		}
	}
	if neg {
		return -total
	}
	return total
}

// unescape reverses RFC 5545 text escaping.
func unescape(s string) string {
	if !strings.ContainsRune(s, '\\') {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' || i+1 >= len(s) {
			b.WriteByte(s[i])
			continue
		}
		i++
		switch s[i] {
		case 'n', 'N':
			b.WriteByte('\n')
		case 't':
			b.WriteByte('\t')
		default:
			b.WriteByte(s[i])
		}
	}
	return b.String()
}
