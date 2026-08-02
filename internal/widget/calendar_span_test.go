package widget

import (
	"encoding/json"
	"fmt"
	"image"
	"testing"
	"time"

	"github.com/dustinahudson/magic-mirror/internal/ics"
	"github.com/dustinahudson/magic-mirror/internal/render"
	"github.com/dustinahudson/magic-mirror/internal/source"
	"github.com/dustinahudson/magic-mirror/internal/store"
)

// The grid used throughout: Sunday 2 August 2026 for four weeks, so
// index 0 is Sun 2 Aug and index 27 is Sat 29 Aug.
var spanGridStart = time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC)

func allDay(summary string, fromDay, days int) ics.Event {
	start := spanGridStart.AddDate(0, 0, fromDay)
	return ics.Event{
		Summary: summary,
		Start:   start,
		// Exclusive, as a real feed writes it.
		End:    start.AddDate(0, 0, days),
		AllDay: true,
		Color:  "#52FA7F",
	}
}

func always(time.Time) bool { return true }

// segKey renders a segment compactly so a whole layout can be asserted in one
// comparison rather than a dozen field checks.
func segKey(s spanSeg) string {
	edge := func(b bool, yes, no string) string {
		if b {
			return yes
		}
		return no
	}
	return fmt.Sprintf("%s r%d c%d-%d L%d %s%s", s.event.Summary, s.row, s.col0, s.col1,
		s.lane, edge(s.fromPrev, "<", "|"), edge(s.toNext, ">", "|"))
}

func segKeys(p spanPlan) []string {
	out := make([]string, 0, len(p.segs))
	for _, s := range p.segs {
		out = append(out, segKey(s))
	}
	return out
}

func wantSegs(t *testing.T, got []string, want ...string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d segments %v, want %d %v", len(got), got, len(want), want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("segment %d = %q, want %q", i, got[i], want[i])
		}
	}
}

// The plain case: an event inside one week is one bar.
func TestSpanWithinOneWeek(t *testing.T) {
	p := planSpans([]ics.Event{allDay("Camping", 1, 3)}, spanGridStart, 4, 4, time.UTC, always)
	wantSegs(t, segKeys(p), "Camping r0 c1-3 L0 ||")
}

// The case the whole exercise is about: an event crossing a week boundary is
// cut into one bar per row, squared off where it continues.
func TestSpanAcrossWeekBoundary(t *testing.T) {
	// Friday of week 0 (index 5) for five days: Fri, Sat | Sun, Mon, Tue.
	p := planSpans([]ics.Event{allDay("Half term", 5, 5)}, spanGridStart, 4, 4, time.UTC, always)
	wantSegs(t, segKeys(p),
		"Half term r0 c5-6 L0 |>",
		"Half term r1 c0-2 L0 <|",
	)
}

// A long event covers whole rows, and the middle rows continue at both ends.
func TestSpanAcrossThreeWeeks(t *testing.T) {
	p := planSpans([]ics.Event{allDay("Away", 3, 16)}, spanGridStart, 4, 4, time.UTC, always)
	wantSegs(t, segKeys(p),
		"Away r0 c3-6 L0 |>",
		"Away r1 c0-6 L0 <>",
		"Away r2 c0-4 L0 <|",
	)
}

// Lanes are assigned over the whole date range, not per row, so a bar stays on
// the same line as it crosses into the next week. Assigning greedily within
// each row lets a bar jump lanes at the boundary and read as two events.
func TestLaneIsStableAcrossRows(t *testing.T) {
	p := planSpans([]ics.Event{
		allDay("Long", 5, 5),  // Fri wk0 -> Tue wk1
		allDay("Short", 0, 3), // Sun-Tue wk0, forces Long off lane 0? no: Long is longer
	}, spanGridStart, 4, 4, time.UTC, always)

	lanes := map[string]map[int]bool{}
	for _, s := range p.segs {
		if lanes[s.event.Summary] == nil {
			lanes[s.event.Summary] = map[int]bool{}
		}
		lanes[s.event.Summary][s.lane] = true
	}
	if len(lanes["Long"]) != 1 {
		t.Errorf("Long occupies lanes %v across rows, want a single lane", lanes["Long"])
	}
}

// Two events sharing a day must not share a lane.
func TestOverlappingSpansGetDifferentLanes(t *testing.T) {
	p := planSpans([]ics.Event{
		allDay("A", 1, 4), // Mon-Thu
		allDay("B", 2, 4), // Tue-Fri
	}, spanGridStart, 4, 4, time.UTC, always)

	byName := map[string]int{}
	for _, s := range p.segs {
		byName[s.event.Summary] = s.lane
	}
	if byName["A"] == byName["B"] {
		t.Errorf("overlapping events both on lane %d", byName["A"])
	}
}

// Events that do not share a day should reuse the top lane rather than
// stacking, or a month of consecutive trips would need a lane each.
func TestNonOverlappingSpansShareALane(t *testing.T) {
	p := planSpans([]ics.Event{
		allDay("First", 0, 2),  // Sun-Mon
		allDay("Second", 3, 2), // Wed-Thu
	}, spanGridStart, 4, 4, time.UTC, always)

	for _, s := range p.segs {
		if s.lane != 0 {
			t.Errorf("%s got lane %d, want 0 — they do not overlap", s.event.Summary, s.lane)
		}
	}
}

// An event that began before the grid starts is squared at the left edge, so
// it does not claim to have started on the first day shown.
func TestSpanClippedAtGridStart(t *testing.T) {
	e := allDay("Ongoing", -3, 6) // starts 3 days before the grid, ends Tue
	p := planSpans([]ics.Event{e}, spanGridStart, 4, 4, time.UTC, always)
	wantSegs(t, segKeys(p), "Ongoing r0 c0-2 L0 <|")
}

func TestSpanClippedAtGridEnd(t *testing.T) {
	e := allDay("Rolls on", 26, 10) // starts Fri of the last row, runs past the end
	p := planSpans([]ics.Event{e}, spanGridStart, 4, 4, time.UTC, always)
	wantSegs(t, segKeys(p), "Rolls on r3 c5-6 L0 |>")
}

func TestSpanEntirelyOutsideTheGridIsDropped(t *testing.T) {
	p := planSpans([]ics.Event{
		allDay("Last month", -20, 5),
		allDay("Next month", 40, 5),
	}, spanGridStart, 4, 4, time.UTC, always)
	if len(p.segs) != 0 {
		t.Errorf("got %v, want nothing drawn", segKeys(p))
	}
}

// In month view the leading and trailing cells are blank, so a bar has to stop
// at the edge of the month rather than run out over empty cells.
func TestSpanStopsAtTheEdgeOfAMonthView(t *testing.T) {
	// Grid starting Sun 28 June 2026, with only July visible.
	start := time.Date(2026, 6, 28, 0, 0, 0, 0, time.UTC)
	julyOnly := func(d time.Time) bool { return d.Month() == time.July }

	// Runs 29 June to 3 July: two days before the month, three inside it.
	e := ics.Event{
		Summary: "Crossover", AllDay: true, Color: "#52FA7F",
		Start: time.Date(2026, 6, 29, 0, 0, 0, 0, time.UTC),
		End:   time.Date(2026, 7, 4, 0, 0, 0, 0, time.UTC),
	}
	p := planSpans([]ics.Event{e}, start, 5, 4, time.UTC, julyOnly)
	// 1 July is index 3 (Wed), 3 July is index 5 (Fri).
	wantSegs(t, segKeys(p), "Crossover r0 c3-5 L0 <|")
}

// Beyond the lane budget an event is not silently dropped: every day it
// covers is told, so those days can show it in their "+N more".
func TestSpansBeyondTheLaneBudgetAreCounted(t *testing.T) {
	var events []ics.Event
	for i := range 4 {
		events = append(events, allDay(fmt.Sprintf("Trip %d", i), 1, 3)) // all Mon-Wed
	}
	p := planSpans(events, spanGridStart, 4, 2, time.UTC, always)

	if got := len(p.segs); got != 2*1 {
		t.Errorf("drew %d segments, want 2 (the lane budget)", got)
	}
	// Mon, Tue and Wed each hide two events.
	for _, day := range []string{"2026-08-03", "2026-08-04", "2026-08-05"} {
		if p.hidden[day] != 2 {
			t.Errorf("hidden[%s] = %d, want 2", day, p.hidden[day])
		}
	}
	if p.hidden["2026-08-06"] != 0 {
		t.Errorf("Thursday is not covered by any of them: hidden = %d", p.hidden["2026-08-06"])
	}
}

// Only the days a bar actually covers give up space to it.
//
// The reservation used to be per row, which pushed the chips of every other
// day in that week down a line to sit under a bar that never touched them —
// visible on a real mirror as a blank gap above an unrelated Tuesday.
func TestOnlyCoveredDaysReserveSpace(t *testing.T) {
	// Monday to Wednesday of the first week.
	p := planSpans([]ics.Event{allDay("Camping", 1, 3)}, spanGridStart, 4, 4, time.UTC, always)

	for _, day := range []string{"2026-08-03", "2026-08-04", "2026-08-05"} {
		if got := p.laneCount(day); got != 1 {
			t.Errorf("%s is under the bar but reserves %d lanes, want 1", day, got)
		}
	}
	// Sunday before it, Thursday after it, and a day in another week.
	for _, day := range []string{"2026-08-02", "2026-08-06", "2026-08-11"} {
		if got := p.laneCount(day); got != 0 {
			t.Errorf("%s has no bar over it but reserves %d lanes", day, got)
		}
	}
}

// A day sitting under the deeper of two overlapping bars has to clear both,
// even though the top lane is empty above it on that particular day.
func TestReservationClearsTheDeepestBarOverThatDay(t *testing.T) {
	p := planSpans([]ics.Event{
		allDay("Long", 0, 5),  // Sun-Thu, takes lane 0
		allDay("Short", 3, 2), // Wed-Thu, pushed to lane 1
	}, spanGridStart, 4, 4, time.UTC, always)

	if got := p.laneCount("2026-08-05"); got != 2 { // Wednesday: both bars
		t.Errorf("Wednesday reserves %d lanes, want 2", got)
	}
	if got := p.laneCount("2026-08-02"); got != 1 { // Sunday: only the long one
		t.Errorf("Sunday reserves %d lanes, want 1", got)
	}
}

// DTEND is exclusive. A single-day all-day event must not become a two-day
// bar, which would be the most common event in most feeds rendered wrong.
func TestSingleDayAllDayIsNotASpan(t *testing.T) {
	e := allDay("Bin day", 2, 1) // 4 Aug 00:00 -> 5 Aug 00:00
	first, last := eventDays(e, time.UTC)
	if !first.Equal(last) {
		t.Errorf("occupies %s..%s, want a single day", first.Format("Jan 2"), last.Format("Jan 2"))
	}
	if spansDays(e, time.UTC) {
		t.Error("treated as a span")
	}
	if p := planSpans([]ics.Event{e}, spanGridStart, 4, 4, time.UTC, always); len(p.segs) != 0 {
		t.Errorf("drew a bar for a one-day event: %v", segKeys(p))
	}
}

func TestTwoDayAllDayIsASpan(t *testing.T) {
	e := allDay("Weekend away", 6, 2) // Sat + Sun
	if !spansDays(e, time.UTC) {
		t.Fatal("a two-day event should span")
	}
	p := planSpans([]ics.Event{e}, spanGridStart, 4, 4, time.UTC, always)
	wantSegs(t, segKeys(p),
		"Weekend away r0 c6-6 L0 |>",
		"Weekend away r1 c0-0 L0 <|",
	)
}

// A timed event is never a bar, however long it runs — the bar has no room
// for the times, which are the point of a timed event.
func TestTimedEventsAreNeverSpans(t *testing.T) {
	e := ics.Event{
		Summary: "Conference",
		Start:   spanGridStart.AddDate(0, 0, 1).Add(9 * time.Hour),
		End:     spanGridStart.AddDate(0, 0, 3).Add(17 * time.Hour),
	}
	if spansDays(e, time.UTC) {
		t.Error("a multi-day timed event was treated as a bar")
	}
}

// A malformed feed with DTEND == DTSTART must not produce a negative range.
func TestZeroLengthAllDayIsOneDay(t *testing.T) {
	day := time.Date(2026, 8, 5, 0, 0, 0, 0, time.UTC)
	e := ics.Event{Summary: "Odd", Start: day, End: day, AllDay: true}
	first, last := eventDays(e, time.UTC)
	if !first.Equal(day) || !last.Equal(day) {
		t.Errorf("got %s..%s, want both %s", first, last, day)
	}
}

// Ordering must not depend on the order events arrive from the feed, or the
// grid would reshuffle its lanes between refreshes.
func TestLaneAssignmentIsStableRegardlessOfInputOrder(t *testing.T) {
	a, b, c := allDay("Alpha", 1, 5), allDay("Bravo", 2, 3), allDay("Charlie", 3, 6)
	first := segKeys(planSpans([]ics.Event{a, b, c}, spanGridStart, 4, 4, time.UTC, always))
	second := segKeys(planSpans([]ics.Event{c, a, b}, spanGridStart, 4, 4, time.UTC, always))
	third := segKeys(planSpans([]ics.Event{b, c, a}, spanGridStart, 4, 4, time.UTC, always))

	for i := range first {
		if first[i] != second[i] || first[i] != third[i] {
			t.Fatalf("layout depends on input order:\n  %v\n  %v\n  %v", first, second, third)
		}
	}
}

// Bars are the only thing here that draws across cell boundaries, so they are
// the thing most able to escape the tile. The compositor clears only the
// tile's own rectangle, so a stray pixel persists as litter over a neighbour.
func TestSpansStayInsideTheTile(t *testing.T) {
	loc := time.UTC
	now := time.Date(2026, 8, 5, 9, 0, 0, 0, loc)
	weekStart := time.Date(2026, 8, 2, 0, 0, 0, 0, loc)
	day := func(n int) time.Time { return weekStart.AddDate(0, 0, n) }

	var events []ics.Event
	// Deliberately awkward: running off both ends of the grid, overlapping,
	// and more of them than the lane budget can hold.
	for i := range 8 {
		events = append(events, ics.Event{
			Summary: fmt.Sprintf("Span %d", i), AllDay: true, Color: "#52FA7F",
			Start: day(-3 + i), End: day(-3 + i + 12),
		})
	}

	s := store.New()
	s.Success(source.KeyCalendar, source.CalendarData{Events: events})
	ctx := Context{Now: now, Loc: loc, Fonts: render.NewFontSet(), Data: s.Load()}

	for _, size := range []image.Point{{900, 600}, {520, 300}, {300, 160}, {160, 90}} {
		t.Run(fmt.Sprintf("%dx%d", size.X, size.Y), func(t *testing.T) {
			const margin = 20
			dst := image.NewRGBA(image.Rect(0, 0, size.X+2*margin, size.Y+2*margin))
			tile := image.Rect(margin, margin, margin+size.X, margin+size.Y)

			for _, mode := range []string{"rolling", "month"} {
				cfg := fmt.Sprintf(`{"mode":%q,"weeks":4}`, mode)
				Build("calendar", json.RawMessage(cfg)).Render(dst, tile, ctx)
				if n := lit(dst, dst.Bounds()) - lit(dst, tile); n != 0 {
					t.Errorf("%s: %d pixels drawn outside the tile", mode, n)
				}
			}
		})
	}
}

// Days either side of a DST change must still count as one day each, or a bar
// crossing the change would come up a day short.
func TestSpanAcrossADSTChange(t *testing.T) {
	loc, err := time.LoadLocation("America/Chicago")
	if err != nil {
		t.Skip("no tzdata")
	}
	// US DST ends Sunday 1 November 2026.
	start := time.Date(2026, 11, 1, 0, 0, 0, 0, loc)
	e := ics.Event{
		Summary: "Fall back", AllDay: true, Color: "#52FA7F",
		Start: time.Date(2026, 10, 31, 0, 0, 0, 0, loc),
		End:   time.Date(2026, 11, 4, 0, 0, 0, 0, loc), // exclusive: through 3 Nov
	}
	p := planSpans([]ics.Event{e}, start, 2, 4, loc, always)
	// 31 Oct is before the grid, so the bar runs Sun 1 Nov to Tue 3 Nov.
	wantSegs(t, segKeys(p), "Fall back r0 c0-2 L0 <|")
}
