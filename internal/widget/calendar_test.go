package widget

import (
	"strings"
	"testing"
	"time"

	"github.com/dustinahudson/magic-mirror/internal/ics"
	"github.com/dustinahudson/magic-mirror/internal/render"
)

// A day cell is narrow. These tests use a real face at the size and width a
// full-screen calendar actually picks — nine grid columns of 1920 over seven
// days, less padding, and drawEvents' 18px ceiling in a tall cell — so what
// wraps here is what wraps on the mirror.
const (
	testCellW  = 195
	testFontPx = 18
)

func testFace(t *testing.T) *render.Face {
	t.Helper()
	f, err := render.NewFontSet().Face(render.Regular, testFontPx)
	if err != nil {
		t.Fatalf("face: %v", err)
	}
	return f
}

func timed(summary string, hour int) ics.Event {
	return ics.Event{
		Summary: summary,
		Start:   time.Date(2026, 7, 30, hour, 0, 0, 0, time.UTC),
		Color:   "#52FA7F",
	}
}

func plan(t *testing.T, events []ics.Event, room int) ([]eventChip, int) {
	t.Helper()
	face := testFace(t)
	return planEvents(events, face, testCellW, face.Height()*2/3, room, time.UTC)
}

// The reported symptom: a title long enough to need the room should use it
// rather than ellipsising on line one.
func TestPlanEventsWrapsRatherThanTruncating(t *testing.T) {
	chips, overflow := plan(t, []ics.Event{timed("Coffee with Derrick", 9)}, 8)
	if len(chips) != 1 {
		t.Fatalf("got %d chips, want 1", len(chips))
	}
	if overflow != 0 {
		t.Errorf("overflow = %d, want 0", overflow)
	}
	if len(chips[0].lines) < 2 {
		t.Fatalf("title did not wrap: %q", chips[0].lines)
	}
	if joined := strings.Join(chips[0].lines, " "); !strings.Contains(joined, "Derrick") {
		t.Errorf("wrapped text lost the distinguishing word: %q", joined)
	}
	for _, l := range chips[0].lines {
		if strings.Contains(l, "…") {
			t.Errorf("ellipsised despite fitting in %d lines: %q", maxEventLines, chips[0].lines)
		}
	}
}

// The cap is the point: one event must not be able to swallow a day.
func TestPlanEventsCapsAtThreeLines(t *testing.T) {
	long := timed(strings.Repeat("Quarterly planning review ", 12), 9)
	chips, _ := plan(t, []ics.Event{long}, 20)
	if len(chips) != 1 {
		t.Fatalf("got %d chips, want 1", len(chips))
	}
	if got := len(chips[0].lines); got != maxEventLines {
		t.Fatalf("lines = %d, want %d", got, maxEventLines)
	}
	if last := chips[0].lines[maxEventLines-1]; !strings.HasSuffix(last, "…") {
		t.Errorf("cut text is not marked as cut: %q", last)
	}
}

// Wrapping costs lines, so fewer events fit. What is dropped must still be
// counted — a busy day must not look like a quiet one.
func TestPlanEventsCountsWhatItDrops(t *testing.T) {
	events := []ics.Event{
		timed("Coffee with Derrick", 9),
		timed("Office Hours", 10),
		timed("Standup", 11),
		timed("Retrospective and planning session", 14),
	}
	chips, overflow := plan(t, events, 4)

	used := 0
	for _, c := range chips {
		used += len(c.lines)
	}
	if used+1 > 4 {
		t.Errorf("no line left for the +%d more marker: used %d of 4", overflow, used)
	}
	if len(chips)+overflow != len(events) {
		t.Errorf("%d shown + %d overflow != %d events", len(chips), overflow, len(events))
	}
	if overflow == 0 {
		t.Error("expected some events not to fit in four lines")
	}
}

// A cell with room for exactly one line must show one event, not a marker
// saying there are events it declined to name.
func TestPlanEventsSingleLineRoom(t *testing.T) {
	chips, overflow := plan(t, []ics.Event{timed("Standup", 9)}, 1)
	if len(chips) != 1 || overflow != 0 {
		t.Fatalf("got %d chips / %d overflow, want 1/0", len(chips), overflow)
	}
}

// An all-day event is a filled block, so its text is inset on both sides and
// wraps at a narrower width than a timed event's.
func TestPlanEventsAllDayWraps(t *testing.T) {
	e := ics.Event{Summary: "Independence Day observed", AllDay: true, Color: "#FF5252"}
	chips, _ := plan(t, []ics.Event{e}, 8)
	if len(chips) != 1 {
		t.Fatalf("got %d chips, want 1", len(chips))
	}
	if len(chips[0].lines) < 2 {
		t.Errorf("all-day title did not wrap: %q", chips[0].lines)
	}
}

// An unparseable calendar colour must not drop the event.
func TestPlanEventsBadColourStillShows(t *testing.T) {
	e := timed("Standup", 9)
	e.Color = "not-a-colour"
	chips, _ := plan(t, []ics.Event{e}, 4)
	if len(chips) != 1 {
		t.Fatalf("got %d chips, want 1", len(chips))
	}
	if chips[0].color != render.Secondary {
		t.Errorf("colour = %v, want the Secondary fallback", chips[0].color)
	}
}
