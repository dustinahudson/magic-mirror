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
		Type:        "calendar",
		Name:        "Month Calendar",
		Description: "Monthly grid with a marker on days that have events.",
		DefaultSpan: layout.Span{Cols: 6, Rows: 8},
		MinSpan:     layout.Span{Cols: 3, Rows: 4},
		Needs:       []SourceKind{SourceCalendar},
		Fields: []Field{
			{
				Key: "feeds", Label: "Calendars", Type: FieldFeeds,
				Help: "Which calendars to mark. Leave empty for all.",
			},
			{
				Key: "weekStart", Label: "Week starts on", Type: FieldSelect,
				Default: "sunday",
				Options: []Option{
					{Value: "sunday", Label: "Sunday"},
					{Value: "monday", Label: "Monday"},
				},
			},
			{Key: "showDots", Label: "Mark days with events", Type: FieldBool, Default: true},
		},
		New: newCalendar,
	})
}

type calendarConfig struct {
	Feeds     []string `json:"feeds"`
	WeekStart string   `json:"weekStart"`
	ShowDots  bool     `json:"showDots"`
}

// Calendar renders a month grid.
//
// Ported from v1's src/modules/widgets/calendar_widget.cpp.
type Calendar struct {
	cfg calendarConfig
}

func newCalendar(raw json.RawMessage) (Widget, error) {
	cfg := calendarConfig{WeekStart: "sunday", ShowDots: true}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &cfg); err != nil {
			return nil, err
		}
	}
	return &Calendar{cfg: cfg}, nil
}

// eventsByDay buckets events onto local dates within the displayed month.
func (w *Calendar) eventsByDay(ctx Context) (map[int][]ics.Event, Staleness, bool) {
	r := store.Get[source.CalendarData](ctx.Data, source.KeyCalendar)
	st := StalenessOf(r, ctx.Now)
	data, ok := r.Get()
	if !ok {
		return nil, st, false
	}

	now := ctx.Local()
	out := map[int][]ics.Event{}
	for _, e := range data.Events {
		if !matchesFeeds(e.FeedID, w.cfg.Feeds) {
			continue
		}
		local := e.Start.In(ctx.Location())
		if local.Year() == now.Year() && local.Month() == now.Month() {
			out[local.Day()] = append(out[local.Day()], e)
		}
	}
	return out, st, true
}

func (w *Calendar) Key(ctx Context) string {
	byDay, st, ok := w.eventsByDay(ctx)
	now := ctx.Local()

	var b strings.Builder
	fmt.Fprintf(&b, "cal|%s|%s|%v", now.Format("2006-01"), st.Key(), ok)
	// Only the set of marked days affects the rendering, not the events
	// themselves, so the key stays small and stable.
	for day := 1; day <= 31; day++ {
		if len(byDay[day]) > 0 {
			fmt.Fprintf(&b, "|%d:%d", day, len(byDay[day]))
		}
	}
	fmt.Fprintf(&b, "|today=%d", now.Day())
	return b.String()
}

func (w *Calendar) Render(dst *image.RGBA, bounds image.Rectangle, ctx Context) {
	byDay, st, _ := w.eventsByDay(ctx)
	now := ctx.Local()

	titleSize := clampInt(bounds.Dy()/12, 14, 32)
	cellSize := clampInt(bounds.Dy()/10, 12, 26)

	titleFace, err := ctx.Fonts.Face(render.SemiBold, titleSize)
	if err != nil {
		return
	}
	dowFace, err := ctx.Fonts.Face(render.Regular, max(10, cellSize*3/4))
	if err != nil {
		return
	}
	dayFace, err := ctx.Fonts.Face(render.Light, cellSize)
	if err != nil {
		return
	}
	todayFace, err := ctx.Fonts.Face(render.SemiBold, cellSize)
	if err != nil {
		return
	}

	st.DrawMarker(dst, bounds, ctx, 13)

	y := bounds.Min.Y
	title := now.Format("January 2006")
	titleFace.DrawTop(dst, bounds.Min.X, y, title, render.Primary)
	y += titleFace.Height() + 8

	mondayFirst := w.cfg.WeekStart == "monday"
	headers := dayHeaders(mondayFirst)

	colW := bounds.Dx() / 7
	for i, h := range headers {
		hw := dowFace.Measure(h)
		cx := bounds.Min.X + i*colW + colW/2
		dowFace.DrawTop(dst, cx-hw/2, y, h, render.Muted)
	}
	y += dowFace.Height() + 6

	// Grid geometry.
	first := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, ctx.Location())
	daysInMonth := time.Date(now.Year(), now.Month()+1, 0, 0, 0, 0, 0, ctx.Location()).Day()

	offset := int(first.Weekday())
	if mondayFirst {
		offset = (offset + 6) % 7
	}

	rows := max(1, (offset+daysInMonth+6)/7)
	availH := bounds.Max.Y - y
	rowH := max(availH/rows, dayFace.Height())

	// Reserve room under each numeral for the event markers, so a busy day
	// does not push its dots into the row below.
	const dotRadius = 2
	dotBand := 0
	if w.cfg.ShowDots {
		dotBand = dotRadius*2 + 3
	}

	for day := 1; day <= daysInMonth; day++ {
		idx := offset + day - 1
		col, row := idx%7, idx/7

		cy := y + row*rowH
		if cy >= bounds.Max.Y {
			// The month is taller than the space it was given. Stop rather
			// than drawing over whatever sits below.
			break
		}
		cx := bounds.Min.X + col*colW + colW/2

		isToday := day == now.Day()
		face := dayFace
		fg := render.Secondary
		if isToday {
			face = todayFace
			fg = render.Background
		}

		label := fmt.Sprintf("%d", day)
		lw := face.Measure(label)
		textY := cy + (rowH-dotBand-face.Height())/2

		if isToday {
			// Today gets a filled disc with the numeral knocked out of it.
			r := min(colW, rowH-dotBand)/2 - 2
			if r > 0 {
				render.FillCircle(dst, cx, textY+face.Height()/2, r, render.Primary)
			}
		}

		face.DrawTop(dst, cx-lw/2, textY, label, fg)

		if dotBand > 0 && len(byDay[day]) > 0 && !isToday {
			dotY := textY + face.Height() + dotRadius
			if dotY+dotRadius <= bounds.Max.Y {
				w.drawDots(dst, byDay[day], cx, dotY, dotRadius)
			}
		}
	}
}

// drawDots marks a day with one dot per calendar that has an event, in that
// calendar's colour, capped so a busy day stays legible.
func (w *Calendar) drawDots(dst *image.RGBA, events []ics.Event, cx, y, r int) {
	seen := map[string]bool{}
	var colors []render.RGBA
	for _, e := range events {
		if seen[e.FeedID] {
			continue
		}
		seen[e.FeedID] = true
		c, ok := render.ParseHexColor(e.Color)
		if !ok {
			c = render.Secondary
		}
		colors = append(colors, c)
		if len(colors) == 3 {
			break
		}
	}
	if len(colors) == 0 {
		return
	}

	gap := r*2 + 3
	total := len(colors)*gap - 3
	x := cx - total/2 + r

	for _, c := range colors {
		render.FillCircle(dst, x, y, r, c)
		x += gap
	}
}

func dayHeaders(mondayFirst bool) []string {
	base := []string{"S", "M", "T", "W", "T", "F", "S"}
	if !mondayFirst {
		return base
	}
	return []string{"M", "T", "W", "T", "F", "S", "S"}
}

// matchesFeeds reports whether an event's feed is selected. An empty
// selection means all feeds.
func matchesFeeds(feedID string, selected []string) bool {
	if len(selected) == 0 {
		return true
	}
	for _, s := range selected {
		if s == feedID {
			return true
		}
	}
	return false
}
