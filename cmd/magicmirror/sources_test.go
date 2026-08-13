package main

import (
	"testing"
	"time"

	"github.com/dustinahudson/magic-mirror/internal/config"
	"github.com/dustinahudson/magic-mirror/internal/source"
)

// buildSources decides what the mirror fetches. Getting it wrong produces a
// widget with nothing behind it — which looks exactly like a broken feed, a
// broken network or a broken mirror, and is diagnosed by fetching the card.

func keysOf(fetchers []source.Fetcher) map[string]bool {
	out := map[string]bool{}
	for _, f := range fetchers {
		out[f.Key()] = true
	}
	return out
}

// The system source is unconditional: it publishes the IP printed in the
// corner of every frame, which is the only way anyone finds the settings page
// when the mirror is misbehaving.
func TestSystemSourceIsAlwaysBuilt(t *testing.T) {
	cfg := config.Config{}
	got := keysOf(buildSources(cfg, time.UTC))
	if !got[source.KeySystem] {
		t.Error("no system source; the mirror would never show its own address")
	}
}

func TestCalendarSourceOnlyWhenThereAreFeeds(t *testing.T) {
	cases := []struct {
		name  string
		feeds []config.Feed
		want  bool
	}{
		{"none", nil, false},
		{"one", []config.Feed{{ID: "a", URL: "https://example.invalid/a.ics"}}, true},
		{
			"several",
			[]config.Feed{
				{ID: "a", URL: "https://example.invalid/a.ics"},
				{ID: "b", URL: "https://example.invalid/b.ics"},
			},
			true,
		},
		// A feed row with no URL is one somebody is midway through adding.
		// It must not create a fetcher, and must not suppress the others.
		{"blank url only", []config.Feed{{ID: "a"}}, false},
		{
			"blank url alongside a real one",
			[]config.Feed{{ID: "a"}, {ID: "b", URL: "https://example.invalid/b.ics"}},
			true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.Config{Calendars: tc.feeds}
			if got := keysOf(buildSources(cfg, time.UTC))[source.KeyCalendar]; got != tc.want {
				t.Errorf("calendar source built = %v, want %v", got, tc.want)
			}
		})
	}
}

// Adding a calendar has to change what gets fetched. This is the shape of the
// bug that put a mirror in a car: the config grew a feed and the fetcher set
// did not.
func TestAddingAFeedChangesTheSourceSet(t *testing.T) {
	before := config.Config{Calendars: []config.Feed{
		{ID: "a", URL: "https://example.invalid/a.ics"},
	}}
	after := config.Config{Calendars: []config.Feed{
		{ID: "a", URL: "https://example.invalid/a.ics"},
		{ID: "b", URL: "https://example.invalid/b.ics"},
	}}

	if !keysOf(buildSources(before, time.UTC))[source.KeyCalendar] {
		t.Fatal("no calendar source to begin with")
	}
	if !keysOf(buildSources(after, time.UTC))[source.KeyCalendar] {
		t.Fatal("calendar source disappeared when a feed was added")
	}

	// Same key, but the fetcher must carry both feeds — a same-key check
	// alone would pass while the new feed went unfetched forever.
	got := buildSources(after, time.UTC)
	var cal *source.CalendarSource
	for _, f := range got {
		if c, ok := f.(*source.CalendarSource); ok {
			cal = c
		}
	}
	if cal == nil {
		t.Fatal("no *source.CalendarSource in the built set")
	}
	if len(cal.Feeds) != 2 {
		t.Errorf("calendar carries %d feeds, want 2", len(cal.Feeds))
	}
}

func TestWeatherSourceFromZipOrCoordinates(t *testing.T) {
	cases := []struct {
		name string
		w    config.Weather
		want bool
	}{
		{"nothing configured", config.Weather{}, false},
		{"zipcode", config.Weather{Zipcode: "64111"}, true},
		{"coordinates", config.Weather{Latitude: 39.05, Longitude: -94.59}, true},
		{"both", config.Weather{Zipcode: "64111", Latitude: 39.05, Longitude: -94.59}, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.Config{Weather: tc.w}
			if got := keysOf(buildSources(cfg, time.UTC))[source.KeyWeather]; got != tc.want {
				t.Errorf("weather source built = %v, want %v", got, tc.want)
			}
		})
	}
}

// Every networked source must declare itself, or the startup gate lets it
// fetch before the radio has associated — which is what pushed the first
// calendar out to the five minute mark.
func TestNetworkedSourcesDeclareThemselves(t *testing.T) {
	cfg := config.Config{
		Weather:   config.Weather{Zipcode: "64111"},
		Calendars: []config.Feed{{ID: "a", URL: "https://example.invalid/a.ics"}},
	}

	for _, f := range buildSources(cfg, time.UTC) {
		n, ok := f.(interface{ NeedsNetwork() bool })
		switch f.Key() {
		case source.KeyWeather, source.KeyCalendar:
			if !ok || !n.NeedsNetwork() {
				t.Errorf("%s does not declare that it needs the network", f.Key())
			}
		case source.KeySystem:
			// Must not wait: it publishes the address someone needs while
			// they are still setting the wifi up.
			if ok && n.NeedsNetwork() {
				t.Error("the system source waits for a network it does not need")
			}
		}
	}
}

// A default config has to produce a working mirror, because it is what the
// device falls back to when its own config cannot be read.
func TestDefaultConfigProducesUsableSources(t *testing.T) {
	got := keysOf(buildSources(config.Default(), time.UTC))
	if !got[source.KeySystem] {
		t.Error("defaults produce no system source")
	}
	if !got[source.KeyWeather] {
		t.Error("defaults produce no weather source, so a fallback mirror shows no conditions")
	}
}

// Fetchers must have sane intervals and timeouts. A zero interval is a
// hot loop on a single-core device; a zero timeout is the hang that used to
// trip the watchdog and reboot the mirror.
func TestFetchersHaveSaneTimings(t *testing.T) {
	cfg := config.Config{
		Weather:   config.Weather{Zipcode: "64111"},
		Calendars: []config.Feed{{ID: "a", URL: "https://example.invalid/a.ics"}},
	}

	for _, f := range buildSources(cfg, time.UTC) {
		if f.Interval() <= 0 {
			t.Errorf("%s has interval %v; that is a hot loop", f.Key(), f.Interval())
		}
		if f.Timeout() <= 0 {
			t.Errorf("%s has timeout %v; that is an unbounded hang", f.Key(), f.Timeout())
		}
		if f.Timeout() > f.Interval() {
			t.Errorf("%s can take %v but is asked every %v, so attempts overlap",
				f.Key(), f.Timeout(), f.Interval())
		}
	}
}
