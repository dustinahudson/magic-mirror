// Package widget defines what a mirror tile is and how it describes its own
// configuration.
//
// The registry exists so the config web UI never needs a hand-written form
// per widget type. A widget declares its fields; the UI renders them
// generically. Adding a widget type is one Go file and zero web changes —
// otherwise it becomes a two-language change and stops happening.
package widget

import (
	"encoding/json"
	"fmt"
	"image"
	"sort"
	"sync"
	"time"

	"github.com/dustinahudson/magic-mirror/internal/layout"
	"github.com/dustinahudson/magic-mirror/internal/render"
	"github.com/dustinahudson/magic-mirror/internal/store"
)

// Context is everything a widget may read while rendering.
//
// Note what is absent: no HTTP client, no source handles, no config writer.
// A widget physically cannot perform I/O during Render, which is what keeps
// the render loop unstallable.
type Context struct {
	Now   time.Time
	Loc   *time.Location
	Fonts *render.FontSet
	Data  *store.Snapshot

	// Units is "imperial" or "metric".
	Units string
}

// Metric reports whether to render in metric units.
func (c Context) Metric() bool { return c.Units == "metric" }

// Local returns Now in the configured timezone.
func (c Context) Local() time.Time {
	if c.Loc == nil {
		return c.Now
	}
	return c.Now.In(c.Loc)
}

// Widget is one tile on the mirror.
type Widget interface {
	// Key returns a value that changes exactly when the rendered output
	// would change, and not otherwise.
	//
	// This is how dirty tracking works. Rather than asking every widget to
	// track its own invalidation — which is easy to get subtly wrong and
	// shows up as a stale tile nobody notices — a widget summarises what it
	// is about to draw, and the compositor redraws only when that summary
	// moves. A clock returns its formatted time; a weather tile returns its
	// temperature, condition and staleness.
	Key(ctx Context) string

	// Render draws the widget into dst, filling bounds.
	//
	// bounds is already cleared to the background. Render must not draw
	// outside it, must not block, and must not perform I/O.
	Render(dst *image.RGBA, bounds image.Rectangle, ctx Context)
}

// SourceKind names a data source a widget depends on. The source manager runs
// one fetcher per unique source regardless of how many widgets want it, so
// two calendar tiles do not double-fetch the same ICS feeds.
type SourceKind string

const (
	SourceWeather  SourceKind = "weather"
	SourceForecast SourceKind = "forecast"
	SourceCalendar SourceKind = "calendar"
	SourceNetwork  SourceKind = "network"
	SourceSystem   SourceKind = "system"
)

// FieldType tells the web UI which control to render.
type FieldType string

const (
	FieldText     FieldType = "text"
	FieldNumber   FieldType = "number"
	FieldBool     FieldType = "bool"
	FieldSelect   FieldType = "select"
	FieldColor    FieldType = "color"
	FieldURL      FieldType = "url"
	FieldDuration FieldType = "duration"
	// FieldFeeds is a multi-select over the configured calendar feeds.
	// Feeds are defined once at top level and referenced by id, so a URL
	// lives in exactly one place.
	FieldFeeds FieldType = "feeds"
)

// Option is one choice in a Select field.
type Option struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

// Field describes one configurable setting. It drives both the generated
// form and server-side validation, so the two cannot drift.
type Field struct {
	Key      string    `json:"key"`
	Label    string    `json:"label"`
	Type     FieldType `json:"type"`
	Default  any       `json:"default,omitempty"`
	Options  []Option  `json:"options,omitempty"`
	Min      *float64  `json:"min,omitempty"`
	Max      *float64  `json:"max,omitempty"`
	Help     string    `json:"help,omitempty"`
	Required bool      `json:"required,omitempty"`
}

// Descriptor is a widget type's self-description.
type Descriptor struct {
	Type        string       `json:"type"`
	Name        string       `json:"name"`
	Description string       `json:"description,omitempty"`
	DefaultSpan layout.Span  `json:"defaultSpan"`
	MinSpan     layout.Span  `json:"minSpan"`
	Fields      []Field      `json:"fields,omitempty"`
	Needs       []SourceKind `json:"needs,omitempty"`

	// New builds an instance from its config blob. The blob is opaque to
	// the core: only this function knows the widget's own schema.
	New func(raw json.RawMessage) (Widget, error) `json:"-"`
}

var (
	regMu    sync.RWMutex
	registry = map[string]Descriptor{}
)

// Register adds a widget type. Called from package init functions.
//
// Panics on a duplicate or malformed descriptor because both are build-time
// mistakes — a binary with two widgets claiming one type name is broken in a
// way no runtime handling improves.
func Register(d Descriptor) {
	if d.Type == "" {
		panic("widget: Register with empty Type")
	}
	if d.New == nil {
		panic(fmt.Sprintf("widget: %s registered without New", d.Type))
	}

	regMu.Lock()
	defer regMu.Unlock()
	if _, dup := registry[d.Type]; dup {
		panic(fmt.Sprintf("widget: duplicate type %q", d.Type))
	}
	if d.DefaultSpan.Cols == 0 {
		d.DefaultSpan.Cols = 3
	}
	if d.DefaultSpan.Rows == 0 {
		d.DefaultSpan.Rows = 3
	}
	if d.MinSpan.Cols == 0 {
		d.MinSpan.Cols = 1
	}
	if d.MinSpan.Rows == 0 {
		d.MinSpan.Rows = 1
	}
	registry[d.Type] = d
}

// Lookup returns the descriptor for a type.
func Lookup(typ string) (Descriptor, bool) {
	regMu.RLock()
	defer regMu.RUnlock()
	d, ok := registry[typ]
	return d, ok
}

// Descriptors returns every registered type, sorted by name, for the web UI.
func Descriptors() []Descriptor {
	regMu.RLock()
	defer regMu.RUnlock()

	out := make([]Descriptor, 0, len(registry))
	for _, d := range registry {
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Build constructs a widget instance from a type name and its config blob.
//
// An unknown type is not an error the caller must handle by failing: it
// yields an Unknown widget that renders a labelled placeholder. That happens
// for real — a config written by a newer binary, then rolled back — and it
// must never prevent the mirror from booting.
func Build(typ string, raw json.RawMessage) Widget {
	d, ok := Lookup(typ)
	if !ok {
		return &Unknown{Type: typ, Reason: "unknown widget type"}
	}
	w, err := d.New(raw)
	if err != nil {
		return &Unknown{Type: typ, Reason: err.Error()}
	}
	return w
}
