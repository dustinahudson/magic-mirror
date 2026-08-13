package widget

import (
	"encoding/json"
	"testing"
	"testing/synctest"
	"time"

	"github.com/dustinahudson/magic-mirror/internal/ics"
	"github.com/dustinahudson/magic-mirror/internal/render"
	"github.com/dustinahudson/magic-mirror/internal/source"
	"github.com/dustinahudson/magic-mirror/internal/store"
)

// Key runs on every frame — that is its job, answering "has anything changed"
// cheaply enough to skip a repaint. Doing that by rebuilding a map and
// formatting a date per event made the check cost more than the change, once
// a second forever, on the single core that is also compositing.
//
// The memo must not buy that at the price of showing stale data, so both
// halves are tested: that it saves the work, and that it cannot go stale.

func calendarWith(t *testing.T, events []ics.Event) (*Calendar, *store.Store) {
	t.Helper()
	w, err := newCalendar(json.RawMessage(`{"mode":"rolling","weeks":4}`))
	if err != nil {
		t.Fatal(err)
	}
	st := store.New()
	st.Success(source.KeyCalendar, source.CalendarData{
		Events:     events,
		FeedErrors: map[string]string{},
	})
	return w.(*Calendar), st
}

func eventAt(summary string, start time.Time) ics.Event {
	return ics.Event{
		UID: summary, Summary: summary,
		Start: start, End: start.Add(time.Hour),
		FeedID: "f1",
	}
}

func ctxFor(data *store.Store, now time.Time) Context {
	return Context{
		Now:   now,
		Loc:   time.UTC,
		Fonts: render.NewFontSet(),
		Data:  data.Load(),
	}
}

// Repeated frames with nothing new must not rebuild the grouping.
func TestCalendarGroupingIsReusedBetweenFrames(t *testing.T) {
	now := time.Date(2026, 8, 12, 9, 0, 0, 0, time.UTC)
	w, data := calendarWith(t, []ics.Event{eventAt("standup", now.Add(2*time.Hour))})

	ctx := ctxFor(data, now)
	first, _, _, _ := w.eventsByDay(ctx)

	// A later frame, same snapshot, same day.
	second, _, _, _ := w.eventsByDay(ctxFor(data, now.Add(30*time.Second)))

	// Same map, not an equal one: a rebuild would allocate a new map.
	if len(first) == 0 {
		t.Fatal("no events grouped at all")
	}
	firstKey := ""
	for k := range first {
		firstKey = k
		break
	}
	first[firstKey] = append(first[firstKey], eventAt("marker", now))
	if len(second[firstKey]) != len(first[firstKey]) {
		t.Error("the grouping was rebuilt for an unchanged frame")
	}
}

// New data must be picked up immediately. The snapshot pointer changing is
// the signal, and it changes on any source's activity.
func TestCalendarGroupingFollowsNewData(t *testing.T) {
	now := time.Date(2026, 8, 12, 9, 0, 0, 0, time.UTC)
	w, data := calendarWith(t, []ics.Event{eventAt("standup", now.Add(2*time.Hour))})

	byDay, _, _, _ := w.eventsByDay(ctxFor(data, now))
	before := 0
	for _, evs := range byDay {
		before += len(evs)
	}

	// Somebody added an event, and the feed came back with it.
	data.Success(source.KeyCalendar, source.CalendarData{
		Events: []ics.Event{
			eventAt("standup", now.Add(2*time.Hour)),
			eventAt("dentist", now.Add(3*time.Hour)),
		},
		FeedErrors: map[string]string{},
	})

	byDay, _, _, _ = w.eventsByDay(ctxFor(data, now))
	after := 0
	for _, evs := range byDay {
		after += len(evs)
	}

	if after <= before {
		t.Errorf("grouped %d events after the update against %d before; "+
			"the memo served stale data", after, before)
	}
}

// Midnight moves the grid even though no data changed, and the day is part
// of the memo key for exactly that reason.
func TestCalendarGroupingFollowsTheDay(t *testing.T) {
	day1 := time.Date(2026, 8, 12, 23, 59, 0, 0, time.UTC)
	w, data := calendarWith(t, []ics.Event{eventAt("standup", day1.Add(2*time.Hour))})

	w.eventsByDay(ctxFor(data, day1))
	dayKeyBefore := w.memoDay

	w.eventsByDay(ctxFor(data, day1.Add(2*time.Minute))) // past midnight
	if w.memoDay == dayKeyBefore {
		t.Error("the memo did not notice the date change")
	}
}

// Staleness is measured against the clock, so it must never be cached: it is
// the one thing that has to keep moving while nothing else does.
func TestStalenessIsNotFrozenByTheMemo(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		now := time.Now()
		w, data := calendarWith(t, []ics.Event{eventAt("standup", now.Add(2*time.Hour))})

		_, _, fresh, _ := w.eventsByDay(ctxFor(data, now))

		// Much later, same snapshot: the grouping is reused, the staleness
		// must not be.
		_, _, later, _ := w.eventsByDay(ctxFor(data, now.Add(6*time.Hour)))

		if fresh.Age == later.Age {
			t.Errorf("age did not advance with the clock: %v then %v", fresh.Age, later.Age)
		}
		if later.Age < 5*time.Hour {
			t.Errorf("age = %v after six hours; the memo froze it", later.Age)
		}
	})
}
