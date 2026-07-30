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
			{
				Key: "showEvents", Label: "Show events in day cells", Type: FieldBool,
				Default: true,
				Help:    "Off shows only a colour dot per calendar, for a short tile.",
			},
		},
		New: newCalendar,
	})
}

type calendarConfig struct {
	Mode       string   `json:"mode"`
	Weeks      int      `json:"weeks"`
	Feeds      []string `json:"feeds"`
	WeekStart  string   `json:"weekStart"`
	ShowEvents bool     `json:"showEvents"`
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
	cfg := calendarConfig{Mode: "rolling", Weeks: 4, WeekStart: "sunday", ShowEvents: true}
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

	// Titles are drawn now, not just counts, so the key has to include them
	// or an edited event would never repaint.
	for i := range rows * 7 {
		key := start.AddDate(0, 0, i).Format("2006-01-02")
		evs := byDay[key]
		if len(evs) == 0 {
			continue
		}
		fmt.Fprintf(&b, "|%d:", i)
		for _, e := range evs {
			fmt.Fprintf(&b, "%s@%s;", e.Summary, e.Start.Format("1504"))
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
	headerPad := clampInt(colW/16, 3, 10)
	for i, h := range dayHeaders(w.mondayFirst()) {
		dowFace.DrawTop(dst, bounds.Min.X+i*colW+headerPad, y, h, render.Muted)
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

	// v1 used pad_all(6) inside each cell; scaled here so it holds up at
	// whatever size the tile ends up.
	pad := clampInt(colW/16, 3, 10)

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

		// Today is a filled cell with light blue numerals, which is what v1
		// did (calendar_widget.cpp:240): bg rgb(50,50,55), text
		// rgb(100,200,255). A white disc with knocked-out text reads as a
		// selection control rather than a date, and on a mirror the softer
		// fill sits back where it belongs.
		cell := image.Rect(
			bounds.Min.X+col*colW, cy,
			bounds.Min.X+(col+1)*colW, min(cy+rowH, bounds.Max.Y),
		)
		if isToday {
			render.Fill(dst, cell.Inset(2), render.CalendarTodayBG)
		}

		// The number sits at the cell's top-left, as v1 had it: cells were a
		// flex column with pad_all(6), so the date led and events stacked
		// beneath it. Centring the number leaves nowhere for anything else
		// to go.
		textX := cell.Min.X + pad
		textY := cy + pad
		face.DrawTop(dst, textX, textY, label, fg)

		if len(byDay[key]) > 0 && w.cfg.ShowEvents {
			evArea := image.Rect(
				cell.Min.X+pad, textY+face.Height()+2,
				cell.Max.X-pad, min(cy+rowH-2, bounds.Max.Y),
			)
			w.drawEvents(dst, byDay[key], evArea, ctx)
		} else if len(byDay[key]) > 0 {
			w.drawDots(dst, byDay[key], textX, textY+face.Height()+2, 2)
		}
	}
}

// maxEventLines caps how far one event's text may wrap inside a day cell.
//
// One line ellipsised early — "9am Coffee with…" — cuts exactly the part that
// distinguishes one meeting from another, and a month cell has the height to
// spare. The cap exists because without it a single long title could swallow
// a day and push everything else out.
const maxEventLines = 3

// eventChip is one event with its text already wrapped to the column.
type eventChip struct {
	event ics.Event
	lines []string
	color render.RGBA
}

// planEvents wraps events to width and decides how many fit in room lines.
//
// Separate from drawing because it is where the decisions are: an event's
// height is now its own, so what fits cannot be known until the text has been
// laid out, and the arithmetic deciding what gets dropped is worth testing
// without a framebuffer.
//
// Returns the chips to draw and how many events were left over.
func planEvents(events []ics.Event, face *render.Face, width, sq, room int,
	loc *time.Location) ([]eventChip, int) {

	var chips []eventChip
	used := 0

	for _, e := range events {
		c, ok := render.ParseHexColor(e.Color)
		if !ok {
			c = render.Secondary
		}

		// All-day text is inset within its filled block; a timed event gives
		// up a gutter to its colour square, and its continuation lines hang
		// under the first so the gutter stays clear.
		text, avail := e.Summary, width-6
		if !e.AllDay {
			t := strings.ToLower(e.Start.In(loc).Format("3:04pm"))
			text = strings.Replace(t, ":00", "", 1) + " " + e.Summary
			avail = width - sq - 4
		}

		lines := face.Wrap(text, avail, maxEventLines)
		if len(lines) == 0 {
			continue
		}
		if used+len(lines) > room {
			break
		}
		chips = append(chips, eventChip{event: e, lines: lines, color: c})
		used += len(lines)
	}

	// Reserve a line for the count, dropping laid-out events until it fits.
	// The count outranks the last event it displaces: it is the only thing
	// telling you the day holds more than what is shown.
	overflow := len(events) - len(chips)
	for overflow > 0 && used+1 > room && len(chips) > 0 {
		used -= len(chips[len(chips)-1].lines)
		chips = chips[:len(chips)-1]
		overflow++
	}
	return chips, overflow
}

// drawEvents stacks event chips under the date, as v1 did.
//
// Two forms, both carried over from v1 (calendar_widget.cpp:420-470):
//
//   - All-day events are a filled block in the calendar's colour, with the
//     text knocked out in black or white depending on how light that colour
//     is. They read as spanning the day, which is what they do.
//   - Timed events are a small colour square followed by the time and title
//     on a transparent background, so a day of meetings stays legible
//     rather than becoming a wall of colour.
//
// A title too long for its column wraps to at most maxEventLines lines
// before it is ellipsised. One line ellipsised early — "9am Coffee with…" —
// hides exactly the part that distinguishes one meeting from another, and a
// month cell has the height to spare. The cap exists because without it a
// single long title could fill a day and push everything else out.
//
// Whatever does not fit becomes a "+2 more" line. Silently dropping events
// would make a busy day indistinguishable from a quiet one.
func (w *Calendar) drawEvents(dst *image.RGBA, events []ics.Event, area image.Rectangle, ctx Context) {
	if area.Dy() < 8 || area.Dx() < 20 {
		return
	}

	size := clampInt(area.Dy()/4, 9, 18)
	face, err := ctx.Fonts.Face(render.Regular, size)
	if err != nil {
		return
	}

	lineH := face.Height() + 2
	room := area.Dy() / lineH
	if room < 1 {
		// Not even one chip fits; fall back to colour dots so the day is
		// still marked as busy.
		w.drawDots(dst, events, area.Min.X, area.Min.Y, 2)
		return
	}

	sq := face.Height() * 2 / 3
	chips, overflow := planEvents(events, face, area.Dx(), sq, room, ctx.Location())

	y := area.Min.Y
	for _, ch := range chips {
		if ch.event.AllDay {
			// The block grows to cover every line, so a wrapped all-day
			// event stays one solid chip rather than a stack of bars.
			block := image.Rect(area.Min.X, y, area.Max.X, y+len(ch.lines)*lineH-1)
			render.FillRounded(dst, block, 3, ch.color)
			fg := contrastOn(ch.color)
			for i, line := range ch.lines {
				face.DrawTop(dst, block.Min.X+3, y+i*lineH, line, fg)
			}
		} else {
			render.FillRounded(dst, image.Rect(area.Min.X, y+(face.Height()-sq)/2,
				area.Min.X+sq, y+(face.Height()-sq)/2+sq), 2, ch.color)
			for i, line := range ch.lines {
				face.DrawTop(dst, area.Min.X+sq+4, y+i*lineH, line, render.Secondary)
			}
		}
		y += len(ch.lines) * lineH
	}

	if overflow > 0 && y+face.Height() <= area.Max.Y {
		face.DrawTop(dst, area.Min.X, y,
			fmt.Sprintf("+%d more", overflow), render.Muted)
	}
}

// contrastOn returns black or white, whichever is readable on c.
//
// Perceptual luminance rather than a plain average: a saturated green is far
// lighter to the eye than a saturated blue at the same numeric value, and
// averaging puts white text on both.
func contrastOn(c render.RGBA) render.RGBA {
	lum := (299*int(c.R) + 587*int(c.G) + 114*int(c.B)) / 1000
	if lum > 140 {
		return render.Background
	}
	return render.Primary
}

// drawDots is the fallback for cells too short for chips: one dot per
// calendar with an event that day.
func (w *Calendar) drawDots(dst *image.RGBA, events []ics.Event, x, y, r int) {
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
		if len(colors) == 4 {
			break
		}
	}

	gap := r*2 + 3
	for _, c := range colors {
		render.FillCircle(dst, x+r, y+r, r, c)
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
