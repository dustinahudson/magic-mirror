// Package config loads, validates and saves the mirror's configuration.
//
// Two rules shape this package:
//
//   - Widget settings are opaque. The core never learns a widget's fields;
//     it carries json.RawMessage and lets the widget parse it. Adding a
//     widget type touches no code here.
//   - A bad config never replaces a good one. Load validates before
//     returning, and Save writes atomically, so a mistyped value in the web
//     UI cannot lock you out of the mirror.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/dustinahudson/magic-mirror/internal/durable"
	"github.com/dustinahudson/magic-mirror/internal/ics"
	"github.com/dustinahudson/magic-mirror/internal/layout"
	"github.com/dustinahudson/magic-mirror/internal/update"
	"github.com/dustinahudson/magic-mirror/internal/widget"
)

// Config is the whole mirror configuration.
type Config struct {
	Timezone string `json:"timezone"`

	// Hostname is the name the mirror answers to over mDNS, so
	// <hostname>.local resolves on whatever network it joins. Without the
	// .local suffix.
	Hostname string `json:"hostname"`

	// Units is "imperial" or "metric", applied to every widget that shows a
	// measurement.
	Units string `json:"units"`

	Weather   Weather    `json:"weather"`
	Layout    Layout     `json:"layout"`
	Calendars []Feed     `json:"calendars"`
	Widgets   []Instance `json:"widgets"`
	Update    Update     `json:"update"`
	Web       Web        `json:"web"`
}

// Weather is the location the forecast is fetched for.
type Weather struct {
	Zipcode string `json:"zipcode"`

	// Latitude/Longitude override the zipcode lookup when both are set.
	// Useful outside the US, where the zipcode geocoder is no help.
	Latitude  float64 `json:"latitude,omitempty"`
	Longitude float64 `json:"longitude,omitempty"`
}

// HasCoords reports whether an explicit location was configured.
func (w Weather) HasCoords() bool { return w.Latitude != 0 || w.Longitude != 0 }

// Layout is the grid geometry.
type Layout struct {
	Cols    int `json:"cols"`
	Rows    int `json:"rows"`
	Padding int `json:"padding"`
	Gap     int `json:"gap"`
}

// Grid converts the config into grid geometry.
func (l Layout) Grid() layout.Grid {
	return layout.Grid{
		Cols: l.Cols, Rows: l.Rows,
		PadX: l.Padding, PadY: l.Padding,
		GapX: l.Gap, GapY: l.Gap,
	}
}

// Feed is one calendar source.
//
// Feeds live at top level and widgets reference them by ID. Both the month
// grid and the events list consume the same feeds, so duplicating URLs into
// each widget's config would mean editing a URL in two places and fetching it
// twice.
type Feed struct {
	ID    string `json:"id"`
	URL   string `json:"url"`
	Name  string `json:"name"`
	Color string `json:"color"`
}

// Instance is one placed widget.
type Instance struct {
	ID   string     `json:"id"`
	Type string     `json:"type"`
	Pos  layout.Pos `json:"pos"`

	// Config is opaque here and parsed by the widget itself.
	Config json.RawMessage `json:"config,omitempty"`
}

// Update controls the self-update poller.
type Update struct {
	Enabled bool   `json:"enabled"`
	Repo    string `json:"repo"`
	// Channel is "stable" or "prerelease".
	Channel string `json:"channel"`

	// AllowOS lets the mirror install the kernel and its initramfs, not just
	// the application binary.
	//
	// Off unless asked for, because an OS update only takes effect on the next
	// boot and this device has no safe shutdown — the new kernel sits on the
	// card until somebody pulls the plug. Worth turning on for a mirror nobody
	// can reach with a card reader, since it is the only route an init script
	// or driver fix has to get there at all.
	AllowOS bool `json:"allowOS"`
}

// Web controls the config server.
type Web struct {
	Enabled bool   `json:"enabled"`
	Listen  string `json:"listen"`
}

// Default returns a usable configuration with the v1 widget arrangement.
func Default() Config {
	return Config{
		Timezone: "America/Chicago",
		Hostname: "magicmirror",
		Units:    "imperial",
		Weather:  Weather{Zipcode: "64111"},
		Layout: Layout{
			Cols:    layout.DefaultCols,
			Rows:    layout.DefaultRows,
			Padding: layout.DefaultPadding,
			Gap:     layout.DefaultGap,
		},
		Update: Update{Enabled: true, Repo: "dustinahudson/magic-mirror", Channel: "stable"},
		Web:    Web{Enabled: true, Listen: ":80"},

		// A narrow left rail of clock, conditions, outlook and agenda, with
		// the calendar taking the whole right nine columns at full height.
		//
		// Arrived at by dragging tiles around in the editor on the real
		// panel rather than by reasoning about it here, which is why the
		// proportions are odd numbers: the calendar earns the space because
		// a four-week grid at that size is readable from across a room,
		// and everything else is a glance.
		//
		// No status bar. It reports only unhealthy sources, so on a working
		// mirror it is an empty strip — worth having while bringing the
		// device up, not worth a permanent row afterwards. Add it back from
		// the config page if something starts misbehaving.
		Widgets: []Instance{
			{ID: "clock", Type: "datetime", Pos: layout.Pos{Col: 0, Row: 0, ColSpan: 3, RowSpan: 2}},
			{ID: "weather", Type: "weather", Pos: layout.Pos{Col: 0, Row: 2, ColSpan: 3, RowSpan: 2}},
			{ID: "forecast", Type: "forecast", Pos: layout.Pos{Col: 0, Row: 4, ColSpan: 3, RowSpan: 7}},
			{ID: "events", Type: "upcoming_events", Pos: layout.Pos{Col: 0, Row: 11, ColSpan: 3, RowSpan: 5},
				Config: json.RawMessage(`{"horizonDays":90,"maxEvents":8,"showLocation":false}`)},
			{ID: "calendar", Type: "calendar", Pos: layout.Pos{Col: 3, Row: 0, ColSpan: 9, RowSpan: 16}},
		},
	}
}

// Location resolves the configured timezone, falling back to UTC.
//
// A bad timezone yields UTC and an error rather than a fatal: the clock
// showing the wrong hour is bad, but a mirror that refuses to boot over a
// typo is worse.
func (c Config) Location() (*time.Location, error) {
	if c.Timezone == "" {
		return time.UTC, nil
	}
	loc, err := time.LoadLocation(c.Timezone)
	if err != nil {
		return time.UTC, fmt.Errorf("timezone %q: %w", c.Timezone, err)
	}
	return loc, nil
}

// Feed returns a configured feed by ID.
func (c Config) Feed(id string) (Feed, bool) {
	for _, f := range c.Calendars {
		if f.ID == id {
			return f, true
		}
	}
	return Feed{}, false
}

// Validate checks the configuration and returns every problem found.
//
// Warnings are returned separately from errors: a widget overlapping another
// is worth telling the user about but must not stop the mirror rendering.
func (c Config) Validate() (warnings []string, err error) {
	var errs []string

	if c.Units != "" && c.Units != "imperial" && c.Units != "metric" {
		errs = append(errs, fmt.Sprintf("units %q must be \"imperial\" or \"metric\"", c.Units))
	}
	if _, e := c.Location(); e != nil {
		warnings = append(warnings, e.Error())
	}
	if c.Layout.Cols <= 0 || c.Layout.Rows <= 0 {
		errs = append(errs, "layout cols and rows must both be positive")
	}

	// Upper bounds on what the render loop is asked to do each frame.
	//
	// Neither is reachable through the settings page — the default grid has
	// 192 cells and widgets are added one at a time — so these exist for a
	// config that was hand-edited or arrived damaged. That is worth guarding
	// because of where the cost lands: separator drawing compares every widget
	// against every other on a full repaint, so the work grows with the square
	// of the count, on one ARMv6 core. Long enough and a frame outlasts the
	// thirty second watchdog, which reboots into the same config and does it
	// again. A boot loop is the one failure this device cannot be talked out
	// of remotely.
	//
	// Rejecting instead means the mirror falls back to the last good config,
	// or to defaults, and stays reachable to be told otherwise.
	const (
		maxWidgets   = 256
		maxGridSide  = 512
		maxCalendars = 64
	)
	if len(c.Widgets) > maxWidgets {
		errs = append(errs, fmt.Sprintf("%d widgets is more than the %d this display can draw",
			len(c.Widgets), maxWidgets))
	}
	if c.Layout.Cols > maxGridSide || c.Layout.Rows > maxGridSide {
		errs = append(errs, fmt.Sprintf("layout %dx%d exceeds the %d cell limit per side",
			c.Layout.Cols, c.Layout.Rows, maxGridSide))
	}
	if len(c.Calendars) > maxCalendars {
		errs = append(errs, fmt.Sprintf("%d calendars is more than the %d this mirror will fetch",
			len(c.Calendars), maxCalendars))
	}

	seenFeed := map[string]bool{}
	for i, f := range c.Calendars {
		switch {
		case f.ID == "":
			errs = append(errs, fmt.Sprintf("calendars[%d]: missing id", i))
		case seenFeed[f.ID]:
			errs = append(errs, fmt.Sprintf("calendars[%d]: duplicate id %q", i, f.ID))
		}
		seenFeed[f.ID] = true
		if f.URL == "" {
			warnings = append(warnings, fmt.Sprintf("calendar %q has no URL and will be skipped", f.ID))
		} else if u := ics.NormalizeURL(f.URL); !strings.HasPrefix(u, "http://") &&
			!strings.HasPrefix(u, "https://") {
			// Judged after normalisation, or this rejects the webcal:// link
			// Apple's share sheet hands out — the exact string a user is most
			// likely to paste.
			errs = append(errs, fmt.Sprintf("calendar %q: link must be a web address", f.ID))
		}
	}

	seenWidget := map[string]bool{}
	for i, w := range c.Widgets {
		switch {
		case w.ID == "":
			errs = append(errs, fmt.Sprintf("widgets[%d]: missing id", i))
		case seenWidget[w.ID]:
			errs = append(errs, fmt.Sprintf("widgets[%d]: duplicate id %q", i, w.ID))
		}
		seenWidget[w.ID] = true

		// An unknown type is a warning, not an error. A config written by a
		// newer binary and then rolled back must still load — the widget
		// renders a labelled placeholder instead.
		if _, ok := widget.Lookup(w.Type); !ok {
			warnings = append(warnings, fmt.Sprintf("widget %q: unknown type %q", w.ID, w.Type))
		}
		if w.Pos.Col < 0 || w.Pos.Row < 0 {
			errs = append(errs, fmt.Sprintf("widget %q: position must not be negative", w.ID))
		}
		if w.Pos.Col >= c.Layout.Cols || w.Pos.Row >= c.Layout.Rows {
			warnings = append(warnings,
				fmt.Sprintf("widget %q sits outside the %dx%d grid and will be clamped",
					w.ID, c.Layout.Cols, c.Layout.Rows))
		}
		for _, other := range c.Widgets[i+1:] {
			if layout.Overlaps(w.Pos, other.Pos) {
				warnings = append(warnings,
					fmt.Sprintf("widgets %q and %q overlap", w.ID, other.ID))
			}
		}
	}

	if len(errs) > 0 {
		return warnings, errors.New(strings.Join(errs, "; "))
	}
	return warnings, nil
}

// Load reads and validates a config file.
func Load(path string) (Config, []string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Config{}, nil, fmt.Errorf("read config: %w", err)
	}
	return Parse(raw)
}

// Parse decodes and validates config bytes.
func Parse(raw []byte) (Config, []string, error) {
	c := Default()
	// Clear the default widget set first: an explicit empty list in the file
	// must mean "no widgets", not "the defaults".
	c.Widgets = nil
	c.Calendars = nil

	dec := json.NewDecoder(strings.NewReader(string(raw)))
	if err := dec.Decode(&c); err != nil {
		return Config{}, nil, fmt.Errorf("parse config: %w", err)
	}

	c.applyV1Compat(raw)
	c.fillDefaults()

	warnings, err := c.Validate()
	if err != nil {
		return Config{}, warnings, err
	}
	return c, warnings, nil
}

// applyV1Compat upgrades a config written by the Circle OS build.
//
// v1's schema (include/config/config.h) had weather.units rather than a
// top-level units, calendars without ids, and no widgets array at all —
// kernel.cpp instantiated four widgets as stack objects. Rather than make
// migration a manual step, read what v1 wrote and fill in what it lacked.
func (c *Config) applyV1Compat(raw []byte) {
	var v1 struct {
		Weather struct {
			Units string `json:"units"`
		} `json:"weather"`
		Calendars []struct {
			URL   string `json:"url"`
			Name  string `json:"name"`
			Color string `json:"color"`
		} `json:"calendars"`
	}
	if err := json.Unmarshal(raw, &v1); err != nil {
		return
	}

	// v1 nested units under weather.
	if c.Units == "" && v1.Weather.Units != "" {
		c.Units = v1.Weather.Units
	}

	// v1 feeds had no ids. Synthesise stable ones from the name, falling
	// back to an index, so widget references survive a rewrite of the file.
	for i, f := range v1.Calendars {
		if i < len(c.Calendars) && c.Calendars[i].ID != "" {
			continue
		}
		id := slug(f.Name)
		if id == "" {
			id = fmt.Sprintf("cal%d", i+1)
		}
		if i < len(c.Calendars) {
			c.Calendars[i].ID = id
		}
	}

	// v1 had no widgets array; give it the default arrangement.
	if len(c.Widgets) == 0 {
		c.Widgets = Default().Widgets
	}
}

func slug(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == ' ' || r == '-' || r == '_':
			b.WriteByte('-')
		}
	}
	return strings.Trim(b.String(), "-")
}

func (c *Config) fillDefaults() {
	d := Default()
	if c.Units == "" {
		c.Units = d.Units
	}
	if c.Timezone == "" {
		c.Timezone = d.Timezone
	}
	if c.Hostname == "" {
		c.Hostname = d.Hostname
	}
	if c.Layout.Cols == 0 {
		c.Layout.Cols = d.Layout.Cols
	}
	if c.Layout.Rows == 0 {
		c.Layout.Rows = d.Layout.Rows
	}
	if c.Layout.Padding == 0 {
		c.Layout.Padding = d.Layout.Padding
	}
	if c.Layout.Gap == 0 {
		c.Layout.Gap = d.Layout.Gap
	}
	if c.Web.Listen == "" {
		c.Web.Listen = d.Web.Listen
	}
	if c.Update.Repo == "" {
		c.Update.Repo = d.Update.Repo
	}

	// Anything not recognised as the test channel becomes stable. Opting a
	// mirror into test builds should take saying so, not a typo.
	if update.IsTestChannel(c.Update.Channel) {
		c.Update.Channel = update.ChannelTest
	} else {
		c.Update.Channel = update.ChannelStable
	}

	// Fill in widget defaults from each type's descriptor, so a config that
	// omits a field gets the same value the form would have shown.
	for i := range c.Widgets {
		w := &c.Widgets[i]
		if w.Pos.ColSpan == 0 || w.Pos.RowSpan == 0 {
			if desc, ok := widget.Lookup(w.Type); ok {
				if w.Pos.ColSpan == 0 {
					w.Pos.ColSpan = desc.DefaultSpan.Cols
				}
				if w.Pos.RowSpan == 0 {
					w.Pos.RowSpan = desc.DefaultSpan.Rows
				}
			}
		}
	}
}

// Save writes the config so that losing power during it leaves the previous
// config intact rather than a truncated one.
//
// Validate first: a config that cannot be loaded is worse than an old one,
// because the mirror reads this file on every boot with nobody in the room.
// See package durable for what "atomically" has to mean on a FAT card.
func Save(path string, c Config) error {
	if _, err := c.Validate(); err != nil {
		return fmt.Errorf("refusing to save invalid config: %w", err)
	}

	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	data = append(data, '\n')

	// Keep the outgoing config as a last-known-good copy before replacing it.
	//
	// Writing atomically is not the same as writing durably, and on this
	// hardware the difference showed. A card that acknowledges a flush it has
	// not honoured loses the file anyway, and what came back was a zero-length
	// config.json with its contents stranded in clusters only fsck could find.
	// The mirror then boots on defaults, and somebody's calendars are gone.
	//
	// Two copies do not make the card honest, but they make one lost write
	// survivable: the fallback costs whatever changed since the previous save
	// rather than everything. Best effort on purpose — failing to write a
	// backup must not stop the real save.
	if prev, err := os.ReadFile(path); err == nil {
		if _, _, perr := Parse(prev); perr == nil {
			_ = durable.WriteFile(BackupPath(path), prev, 0o644)
		}
	}

	return durable.WriteFile(path, data, 0o644)
}

// BackupPath names the last-known-good copy beside a config file.
func BackupPath(path string) string {
	ext := filepath.Ext(path)
	return strings.TrimSuffix(path, ext) + ".prev" + ext
}
