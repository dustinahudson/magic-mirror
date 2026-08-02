package source

import (
	"testing"
	"time"

	"github.com/dustinahudson/magic-mirror/internal/ics"
)

var dedupeDay = time.Date(2026, 8, 3, 0, 0, 0, 0, time.UTC)

// ev builds an event as a feed would deliver it, already tagged with the
// feed's id and colour by fetchFeed.
func ev(feed, colour, uid, summary string, hour int) ics.Event {
	start := dedupeDay.Add(time.Duration(hour) * time.Hour)
	return ics.Event{
		UID: uid, Summary: summary,
		Start: start, End: start.Add(time.Hour),
		FeedID: feed, Color: colour,
	}
}

func summaries(events []ics.Event) []string {
	out := make([]string, 0, len(events))
	for _, e := range events {
		out = append(out, e.Summary+"/"+e.FeedID)
	}
	return out
}

func TestDedupeBySharedUID(t *testing.T) {
	// The same invitation in two accounts keeps its UID even if one person
	// renamed their copy.
	got := dedupeAcrossFeeds([]ics.Event{
		ev("first", "#52FA7F", "abc123", "Parents evening", 18),
		ev("second", "#6EA8FE", "abc123", "School - parents evening", 18),
	})
	if len(got) != 1 {
		t.Fatalf("got %v, want one event", summaries(got))
	}
	if got[0].FeedID != "first" {
		t.Errorf("kept the copy from %q, want the first-listed calendar", got[0].FeedID)
	}
	if got[0].Color != "#52FA7F" {
		t.Errorf("colour is %s, want the first calendar's #52FA7F", got[0].Color)
	}
}

func TestDedupeByMatchingTitle(t *testing.T) {
	// Copied rather than shared: different UIDs, so only the title matches.
	got := dedupeAcrossFeeds([]ics.Event{
		ev("first", "#52FA7F", "uid-a", "Bin day", 7),
		ev("second", "#6EA8FE", "uid-b", "Bin day", 7),
	})
	if len(got) != 1 {
		t.Fatalf("got %v, want one event", summaries(got))
	}
	if got[0].Color != "#52FA7F" {
		t.Errorf("colour is %s, want the first calendar's", got[0].Color)
	}
}

func TestDedupeIgnoresCaseAndSurroundingSpace(t *testing.T) {
	got := dedupeAcrossFeeds([]ics.Event{
		ev("first", "#52FA7F", "", "Swim lessons", 17),
		ev("second", "#6EA8FE", "", "  swim LESSONS ", 17),
	})
	if len(got) != 1 {
		t.Fatalf("got %v, want one event", summaries(got))
	}
}

// The trap: every occurrence of a recurring event carries the same UID, so a
// key without the start time collapses a weekly meeting into one.
func TestRecurringOccurrencesSurvive(t *testing.T) {
	var events []ics.Event
	for week := range 4 {
		e := ev("first", "#52FA7F", "weekly-standup", "Standup", 9)
		e.Start = e.Start.AddDate(0, 0, 7*week)
		e.End = e.Start.Add(time.Hour)
		events = append(events, e)
	}
	if got := dedupeAcrossFeeds(events); len(got) != 4 {
		t.Fatalf("kept %d of 4 occurrences: %v", len(got), summaries(got))
	}
}

// Same title, different times, is two different things — "Office Hours" at
// 10am on Friday and 10am the following Friday, or twice in one day.
func TestSameTitleAtDifferentTimesIsKept(t *testing.T) {
	got := dedupeAcrossFeeds([]ics.Event{
		ev("first", "#52FA7F", "", "Office Hours", 10),
		ev("second", "#6EA8FE", "", "Office Hours", 14),
	})
	if len(got) != 2 {
		t.Fatalf("got %v, want both", summaries(got))
	}
}

// Duplicates within a single calendar are that calendar's business. Two
// bookings really can sit on top of each other, and hiding one would be the
// mirror misreporting the feed it was given.
func TestDuplicatesInsideOneFeedAreKept(t *testing.T) {
	got := dedupeAcrossFeeds([]ics.Event{
		ev("first", "#52FA7F", "", "Viewing", 11),
		ev("first", "#52FA7F", "", "Viewing", 11),
	})
	if len(got) != 2 {
		t.Fatalf("got %v, want both kept", summaries(got))
	}
}

// A third calendar carrying the same event must also lose to the first, not
// merely to whoever it was compared against last.
func TestFirstCalendarWinsAcrossThree(t *testing.T) {
	got := dedupeAcrossFeeds([]ics.Event{
		ev("first", "#52FA7F", "", "Sports day", 9),
		ev("second", "#6EA8FE", "", "Sports day", 9),
		ev("third", "#FAD452", "", "Sports day", 9),
	})
	if len(got) != 1 {
		t.Fatalf("got %v, want one", summaries(got))
	}
	if got[0].FeedID != "first" {
		t.Errorf("kept %q", got[0].FeedID)
	}
}

// An all-day event and a timed one at the same instant are not the same
// thing: midnight-to-midnight is a different commitment from a midnight call.
func TestAllDayDoesNotMatchTimedAtMidnight(t *testing.T) {
	timed := ev("first", "#52FA7F", "", "Handover", 0)
	whole := ev("second", "#6EA8FE", "", "Handover", 0)
	whole.AllDay = true
	whole.End = whole.Start.AddDate(0, 0, 1)

	if got := dedupeAcrossFeeds([]ics.Event{timed, whole}); len(got) != 2 {
		t.Fatalf("got %v, want both", summaries(got))
	}
}

// Untitled events with no UID carry no identity to match on, so they must
// never be folded together.
func TestUntitledEventsAreNeverMerged(t *testing.T) {
	got := dedupeAcrossFeeds([]ics.Event{
		ev("first", "#52FA7F", "", "", 12),
		ev("second", "#6EA8FE", "", "", 12),
	})
	if len(got) != 2 {
		t.Fatalf("got %d, want both untitled events kept", len(got))
	}
}

// Deduping must not disturb anything else in the list.
func TestUnrelatedEventsAreUntouched(t *testing.T) {
	in := []ics.Event{
		ev("first", "#52FA7F", "", "Dentist", 9),
		ev("first", "#52FA7F", "", "Shared", 12),
		ev("second", "#6EA8FE", "", "Shared", 12),
		ev("second", "#6EA8FE", "", "Piano", 17),
	}
	got := dedupeAcrossFeeds(in)
	want := []string{"Dentist/first", "Shared/first", "Piano/second"}
	gotNames := summaries(got)
	if len(gotNames) != len(want) {
		t.Fatalf("got %v, want %v", gotNames, want)
	}
	for i := range want {
		if gotNames[i] != want[i] {
			t.Errorf("[%d] = %q, want %q", i, gotNames[i], want[i])
		}
	}
}

func TestDedupeHandlesEmptyAndSingle(t *testing.T) {
	if got := dedupeAcrossFeeds(nil); len(got) != 0 {
		t.Errorf("nil in, %d out", len(got))
	}
	one := []ics.Event{ev("first", "#52FA7F", "", "Only", 9)}
	if got := dedupeAcrossFeeds(one); len(got) != 1 {
		t.Errorf("single event was dropped")
	}
}
