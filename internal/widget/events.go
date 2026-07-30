package widget

import (
	"encoding/json"
	"fmt"
	"image"
	"strings"
	"time"

	"github.com/dustinahudson/magic-mirror/internal/ics"
	"github.com/dustinahudson/magic-mirror/internal/layout"
	"github.com/dustinahudson/magic-mirror/internal/render"
	"github.com/dustinahudson/magic-mirror/internal/source"
	"github.com/dustinahudson/magic-mirror/internal/store"
)

func init() {
	Register(Descriptor{
		Type:        "upcoming_events",
		Name:        "Upcoming Events",
		Description: "Chronological list of events across the selected calendars.",
		DefaultSpan: layout.Span{Cols: 6, Rows: 8},
		MinSpan:     layout.Span{Cols: 3, Rows: 2},
		Needs:       []SourceKind{SourceCalendar},
		Fields: []Field{
			{
				Key: "feeds", Label: "Calendars", Type: FieldFeeds,
				Help: "Which calendars to list. Leave empty for all.",
			},
			{
				Key: "maxEvents", Label: "Maximum events", Type: FieldNumber,
				Default: 8, Min: f64(1), Max: f64(30),
			},
			{
				Key: "horizonDays", Label: "Look ahead (days)", Type: FieldNumber,
				Default: 14, Min: f64(1), Max: f64(90),
			},
			{Key: "showLocation", Label: "Show location", Type: FieldBool, Default: false},
			{Key: "groupByDay", Label: "Group by day", Type: FieldBool, Default: true},
		},
		New: newUpcomingEvents,
	})
}

type eventsConfig struct {
	Feeds        []string `json:"feeds"`
	MaxEvents    int      `json:"maxEvents"`
	HorizonDays  int      `json:"horizonDays"`
	ShowLocation bool     `json:"showLocation"`
	GroupByDay   bool     `json:"groupByDay"`
}

// UpcomingEvents renders a chronological event list.
//
// Ported from v1's src/modules/widgets/upcoming_events_widget.cpp.
type UpcomingEvents struct {
	cfg eventsConfig
}

func newUpcomingEvents(raw json.RawMessage) (Widget, error) {
	cfg := eventsConfig{MaxEvents: 8, HorizonDays: 14, GroupByDay: true}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &cfg); err != nil {
			return nil, err
		}
	}
	cfg.MaxEvents = clampInt(cfg.MaxEvents, 1, 30)
	cfg.HorizonDays = clampInt(cfg.HorizonDays, 1, 90)
	return &UpcomingEvents{cfg: cfg}, nil
}

func (w *UpcomingEvents) events(ctx Context) ([]ics.Event, Staleness, bool) {
	r := store.Get[source.CalendarData](ctx.Data, source.KeyCalendar)
	st := StalenessOf(r, ctx.Now)
	data, ok := r.Get()
	if !ok {
		return nil, st, false
	}

	horizon := ctx.Now.Add(time.Duration(w.cfg.HorizonDays) * 24 * time.Hour)
	var out []ics.Event
	for _, e := range data.Events {
		if !matchesFeeds(e.FeedID, w.cfg.Feeds) {
			continue
		}
		// Keep events that have not finished yet, so something happening
		// right now stays on screen rather than vanishing at its start time.
		if e.End.Before(ctx.Now) {
			continue
		}
		if e.Start.After(horizon) {
			continue
		}
		out = append(out, e)
		if len(out) >= w.cfg.MaxEvents {
			break
		}
	}
	return out, st, true
}

func (w *UpcomingEvents) Key(ctx Context) string {
	events, st, ok := w.events(ctx)

	var b strings.Builder
	fmt.Fprintf(&b, "events|%s|%v|%d", st.Key(), ok, len(events))
	for _, e := range events {
		fmt.Fprintf(&b, "|%s@%s", e.Summary, e.Start.Format("0102T1504"))
	}
	// Day boundaries change the "Today"/"Tomorrow" labels.
	fmt.Fprintf(&b, "|%s", ctx.Local().Format("2006-01-02"))
	return b.String()
}

func (w *UpcomingEvents) Render(dst *image.RGBA, bounds image.Rectangle, ctx Context) {
	events, st, ok := w.events(ctx)

	// The marker only earns its space when there is data beside it. An
	// empty state already says why it is empty; "waiting…" next to "No
	// calendar data yet" is the same sentence twice.
	if ok && len(events) > 0 {
		st.DrawMarker(dst, bounds, ctx, 13)
	}

	titleSize := clampInt(bounds.Dy()/16, 14, 26)
	timeSize := max(11, titleSize*3/4)

	titleFace, err := ctx.Fonts.Face(render.Regular, titleSize)
	if err != nil {
		return
	}
	metaFace, err := ctx.Fonts.Face(render.Light, timeSize)
	if err != nil {
		return
	}
	dayFace, err := ctx.Fonts.Face(render.SemiBold, timeSize)
	if err != nil {
		return
	}

	if !ok || len(events) == 0 {
		msg := "Nothing scheduled"
		if !ok {
			msg = "No calendar data yet"
			if st.Err != nil {
				msg = firstLine(st.Err.Error())
			}
		}
		metaFace.DrawTop(dst, bounds.Min.X, bounds.Min.Y,
			metaFace.Truncate(msg, bounds.Dx()), render.Faint)
		return
	}

	const barW = 3
	rowGap := 6
	y := bounds.Min.Y
	lastDay := ""

	for _, e := range events {
		local := e.Start.In(ctx.Location())

		if w.cfg.GroupByDay {
			day := relativeDay(local, ctx.Local())
			if day != lastDay {
				if y+dayFace.Height() > bounds.Max.Y {
					break
				}
				if lastDay != "" {
					y += rowGap / 2
				}
				dayFace.DrawTop(dst, bounds.Min.X, y, day, render.Muted)
				y += dayFace.Height() + 3
				lastDay = day
			}
		}

		rowH := titleFace.Height()
		if w.cfg.ShowLocation && e.Location != "" {
			rowH += metaFace.Height()
		}
		if y+rowH > bounds.Max.Y {
			break
		}

		// Colour bar identifying which calendar the event came from.
		c, valid := render.ParseHexColor(e.Color)
		if !valid {
			c = render.Secondary
		}
		render.Fill(dst, image.Rect(bounds.Min.X, y, bounds.Min.X+barW, y+rowH), c)

		x := bounds.Min.X + barW + 8

		// Time on the right, title fills what remains.
		when := formatEventTime(e, local)
		ww := metaFace.Measure(when)
		metaFace.DrawTop(dst, bounds.Max.X-ww, y+(titleFace.Height()-metaFace.Height())/2,
			when, render.Muted)

		titleW := bounds.Max.X - ww - x - 10
		titleFace.DrawTop(dst, x, y, titleFace.Truncate(e.Summary, titleW), render.Primary)
		y += titleFace.Height()

		if w.cfg.ShowLocation && e.Location != "" {
			metaFace.DrawTop(dst, x, y,
				metaFace.Truncate(e.Location, titleW), render.Faint)
			y += metaFace.Height()
		}
		y += rowGap
	}
}

// formatEventTime renders an event's time, collapsing all-day events to a
// label rather than a meaningless midnight.
func formatEventTime(e ics.Event, local time.Time) string {
	if e.AllDay {
		return "all day"
	}
	return strings.ToLower(local.Format("3:04pm"))
}

// relativeDay labels a date relative to today.
func relativeDay(day, now time.Time) string {
	y1, m1, d1 := day.Date()
	y2, m2, d2 := now.Date()
	if y1 == y2 && m1 == m2 && d1 == d2 {
		return "Today"
	}
	tomorrow := now.AddDate(0, 0, 1)
	y3, m3, d3 := tomorrow.Date()
	if y1 == y3 && m1 == m3 && d1 == d3 {
		return "Tomorrow"
	}
	if day.Before(now.AddDate(0, 0, 7)) {
		return day.Format("Monday")
	}
	return day.Format("Mon, Jan 2")
}
