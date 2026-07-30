package widget

import (
	"fmt"
	"image"
	"time"

	"github.com/dustinahudson/magic-mirror/internal/render"
	"github.com/dustinahudson/magic-mirror/internal/store"
)

// Staleness is how a widget shows the health of the data behind it.
//
// The rule this implements: absence is a rendered state, never a fabricated
// value. A widget with no data draws placeholder *chrome* — dashes, a
// skeleton — and never a plausible-looking number. A widget with old data
// draws it, but says it is old.
//
// This is the fix for v1's most misleading behaviour: weather_widget.cpp
// seeded 72°F / "Partly Cloudy" / Dallas in its constructor, so a mirror
// that had never once reached the weather API looked exactly like a working
// one.
type Staleness struct {
	Status store.Status
	Age    time.Duration
	Err    error
}

// StalenessOf summarises a reading for display.
func StalenessOf[T any](r store.Reading[T], now time.Time) Staleness {
	return Staleness{
		Status: r.Status,
		Age:    r.Entry.Age(now),
		Err:    r.LastErr,
	}
}

// Key contributes staleness to a widget's dirty key, so the display updates
// when data health changes even if the values do not.
func (s Staleness) Key() string {
	// Bucket the age so a slowly ageing reading does not repaint every
	// second — only when the displayed label would actually change.
	return fmt.Sprintf("%v/%s", s.Status, s.Label())
}

// Label is the short human-readable staleness marker, empty when data is
// fresh and there is nothing worth saying.
func (s Staleness) Label() string {
	switch s.Status {
	case store.Fresh:
		return ""
	case store.Never:
		return "waiting…"
	case store.Failed:
		return "unavailable"
	default:
		return "as of " + shortAge(s.Age)
	}
}

// Color is the tint for the staleness marker.
func (s Staleness) Color() render.RGBA {
	switch s.Status {
	case store.Failed:
		return render.Error
	case store.Stale:
		return render.Warn
	default:
		return render.Muted
	}
}

// Placeholder is what a numeric field shows when there is no data.
//
// Em dashes rather than a zero: "0°" is a temperature, and a viewer would
// read it as one.
const Placeholder = "—"

// DrawMarker renders the staleness label at the top-right of bounds, if
// there is anything to say. Returns the width consumed.
func (s Staleness) DrawMarker(dst *image.RGBA, bounds image.Rectangle, ctx Context, size int) int {
	label := s.Label()
	if label == "" {
		return 0
	}
	face, err := ctx.Fonts.Face(render.Light, size)
	if err != nil {
		return 0
	}
	w := face.Measure(label)
	face.DrawTop(dst, bounds.Max.X-w, bounds.Min.Y, label, s.Color())
	return w
}

func shortAge(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}
