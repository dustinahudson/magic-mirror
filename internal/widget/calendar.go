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
		Name:        "Calendar",
		Description: "A rolling multi-week window, or a traditional month grid.",
		DefaultSpan: layout.Span{Cols: 6, Rows: 6},
		MinSpan:     layout.Span{Cols: 3, Rows: 3},
		Needs:       []SourceKind{SourceCalendar},
		Fields: []Field{
			{
				Key: "mode", Label: "View", Type: FieldSelect,
				Default: "rolling",
				Options: []Option{
					{Value: "rolling", Label: "Rolling weeks from this week"},
					{Value: "month", Label: "Calendar month"},
				},
				Help: "Rolling keeps today near the top and never wastes rows on the past.",
			},
			{
				Key: "weeks", Label: "Weeks (rolling only)", Type: FieldNumber,
				Default: 4, Min: f64(1), Max: f64(8),
			},
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
	Mode      string   `json:"mode"`
	Weeks     int      `json:"weeks"`
	Feeds     []string `json:"feeds"`
	WeekStart string   `json:"weekStart"`
	ShowDots  bool     `json:"showDots"`
}

// Calendar renders a date grid.
//
// The default is a rolling window starting with the current week rather than
// v1's calendar month. On a mirror, a month grid spends up to four of its six
// rows on days that have already happened; a rolling window keeps today in
// the first row and gives the remaining space to what is coming.
type Calendar struct {
	cfg calendarConfig
}

func newCalendar(raw json.RawMessage) (Widget, error) {
	cfg := calendarConfig{Mode: "rolling", Weeks: 4, WeekStart: "sunday", ShowDots: true}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &cfg); err != nil {
			return nil, err
		}
	}
	cfg.Weeks = clampInt(cfg.Weeks, 1, 8)
	if cfg.Mode != "month" {
		cfg.Mode = "rolling"
	}
	return &Calendar{cfg: cfg}, nil
}

func (w *Calendar) mondayFirst() bool { return w.cfg.WeekStart == "monday" }

// grid returns the first date drawn and how many week rows to draw.
func (w *Calendar) grid(now time.Time) (start time.Time, rows int) {
	if w.cfg.Mode == "month" {
		first := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
		lead := weekIndex(first.Weekday(), w.mondayFirst())
		days := time.Date(now.Year(), now.Month()+1, 0, 0, 0, 0, 0, now.Location()).Day()
		return first.AddDate(0, 0, -lead), (lead + days + 6) / 7
	}

	// Rolling: start at the beginning of the week containing today.
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	return today.AddDate(0, 0, -weekIndex(today.Weekday(), w.mondayFirst())), w.cfg.Weeks
}

// weekIndex is how many days into the week a weekday falls.
func weekIndex(d time.Weekday, mondayFirst bool) int {
	if mondayFirst {
		return (int(d) + 6) % 7
	}
	return int(d)
}

// eventsByDay buckets events by local date.
//
// Keyed by full date rather than day-of-month: a rolling window routinely
// spans two months, and keying on the day number alone would put the 3rd of
// August on the 3rd of July.
func (w *Calendar) eventsByDay(ctx Context) (map[string][]ics.Event, Staleness, bool) {
	r := store.Get[source.CalendarData](ctx.Data, source.KeyCalendar)
	st := StalenessOf(r, ctx.Now)
	data, ok := r.Get()
	if !ok {
		return nil, st, false
	}

	out := map[string][]ics.Event{}
	for _, e := range data.Events {
		if !matchesFeeds(e.FeedID, w.cfg.Feeds) {
			continue
		}
		key := e.Start.In(ctx.Location()).Format("2006-01-02")
		out[key] = append(out[key], e)
	}
	return out, st, true
}

// title names the span being shown.
func (w *Calendar) title(start time.Time, rows int) string {
	if w.cfg.Mode == "month" {
		return start.AddDate(0, 0, 6).Format("January 2006")
	}

	end := start.AddDate(0, 0, rows*7-1)
	switch {
	case start.Year() != end.Year():
		return start.Format("Jan 2006") + " – " + end.Format("Jan 2006")
	case start.Month() != end.Month():
		return start.Format("January") + " – " + end.Format("January 2006")
	default:
		return start.Format("January 2006")
	}
}

func (w *Calendar) Key(ctx Context) string {
	byDay, st, ok := w.eventsByDay(ctx)
	now := ctx.Local()
	start, rows := w.grid(now)

	var b strings.Builder
	fmt.Fprintf(&b, "cal|%s|%d|%s|%v|today=%s",
		start.Format("2006-01-02"), rows, st.Key(), ok, now.Format("2006-01-02"))

	// Only which days are marked affects the drawing, not the events.
	for i := range rows * 7 {
		key := start.AddDate(0, 0, i).Format("2006-01-02")
		if n := len(byDay[key]); n > 0 {
			fmt.Fprintf(&b, "|%d:%d", i, n)
		}
	}
	return b.String()
}

func (w *Calendar) Render(dst *image.RGBA, bounds image.Rectangle, ctx Context) {
	byDay, st, _ := w.eventsByDay(ctx)
	now := ctx.Local()
	today := now.Format("2006-01-02")
	start, rows := w.grid(now)

	titleSize := clampInt(bounds.Dy()/10, 14, 32)
	titleFace, err := ctx.Fonts.FitFace(render.SemiBold, titleSize,
		w.title(start, rows), bounds.Dx(), bounds.Dy()/5)
	if err != nil {
		return
	}
	dowFace, err := ctx.Fonts.Face(render.Regular, max(10, titleFace.Size()*2/3))
	if err != nil {
		return
	}

	st.DrawMarker(dst, bounds, ctx, 13)

	y := bounds.Min.Y
	titleFace.DrawTop(dst, bounds.Min.X, y, w.title(start, rows), render.Primary)
	y += titleFace.Height() + 8

	colW := bounds.Dx() / 7
	for i, h := range dayHeaders(w.mondayFirst()) {
		hw := dowFace.Measure(h)
		dowFace.DrawTop(dst, bounds.Min.X+i*colW+colW/2-hw/2, y, h, render.Muted)
	}
	y += dowFace.Height() + 6

	rowH := max(1, (bounds.Max.Y-y)/rows)
	cellSize := clampInt(rowH/2, 11, 28)

	dayFace, err := ctx.Fonts.Face(render.Light, cellSize)
	if err != nil {
		return
	}
	todayFace, err := ctx.Fonts.Face(render.SemiBold, cellSize)
	if err != nil {
		return
	}

	const dotRadius = 2
	dotBand := 0
	if w.cfg.ShowDots {
		dotBand = dotRadius*2 + 3
	}

	for i := range rows * 7 {
		date := start.AddDate(0, 0, i)
		col, row := i%7, i/7

		// A month view leaves the leading and trailing days blank, the way a
		// wall calendar does. A rolling window has no outside — every cell
		// it shows is a day you care about.
		if w.cfg.Mode == "month" && date.Month() != now.Month() {
			continue
		}

		cy := y + row*rowH
		if cy >= bounds.Max.Y {
			break
		}
		cx := bounds.Min.X + col*colW + colW/2

		key := date.Format("2006-01-02")
		isToday := key == today

		face, fg := dayFace, render.Secondary
		if isToday {
			face, fg = todayFace, render.CalendarToday
		}

		// Label the first of a month, so a rolling window that crosses into
		// August does not silently restart at "1".
		label := fmt.Sprintf("%d", date.Day())
		if date.Day() == 1 && w.cfg.Mode != "month" {
			label = date.Format("Jan 2")
		}

		lw := face.Measure(label)
		textY := cy + (rowH-dotBand-face.Height())/2

		// Today is a filled cell with light blue numerals, which is what v1
		// did (calendar_widget.cpp:240): bg rgb(50,50,55), text
		// rgb(100,200,255). A white disc with knocked-out text reads as a
		// selection control rather than a date, and on a mirror the softer
		// fill sits back where it belongs.
		if isToday {
			cell := image.Rect(
				bounds.Min.X+col*colW, cy,
				bounds.Min.X+(col+1)*colW, min(cy+rowH, bounds.Max.Y),
			)
			render.Fill(dst, cell.Inset(2), render.CalendarTodayBG)
		}
		face.DrawTop(dst, cx-lw/2, textY, label, fg)

		if dotBand > 0 && len(byDay[key]) > 0 && !isToday {
			dotY := textY + face.Height() + dotRadius
			if dotY+dotRadius <= bounds.Max.Y {
				w.drawDots(dst, byDay[key], cx, dotY, dotRadius)
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
	x := cx - (len(colors)*gap-3)/2 + r
	for _, c := range colors {
		render.FillCircle(dst, x, y, r, c)
		x += gap
	}
}

func dayHeaders(mondayFirst bool) []string {
	if mondayFirst {
		return []string{"M", "T", "W", "T", "F", "S", "S"}
	}
	return []string{"S", "M", "T", "W", "T", "F", "S"}
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
