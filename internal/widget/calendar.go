package widget

import (
	"encoding/json"
	"fmt"
	"image"
	"sort"
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

// dayOf truncates a time to its local date.
func dayOf(t time.Time, loc *time.Location) time.Time {
	t = t.In(loc)
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, loc)
}

// eventDays returns the first and last calendar day an event occupies,
// both inclusive.
//
// An all-day event's DTEND is exclusive per RFC 5545 — a single day recorded
// as the 4th to the 5th — so the last day drawn is the day before it. Taking
// DTEND at face value paints every all-day event one day too long, which for
// the single-day case that dominates real calendars means every one of them
// wrongly becomes a two-day span.
func eventDays(e ics.Event, loc *time.Location) (first, last time.Time) {
	first = dayOf(e.Start, loc)
	if !e.AllDay {
		return first, dayOf(e.End, loc)
	}
	last = dayOf(e.End.Add(-time.Nanosecond), loc)
	if last.Before(first) {
		last = first
	}
	return first, last
}

// daysBetween counts whole days from one midnight to another.
//
// Both arguments are midnights in the same location, so the gap is a whole
// number of days — except across a clock change, where it is 23 or 25 hours.
// Rounding to the nearest day absorbs that. Dividing instead truncates toward
// zero, which is wrong twice: it loses the short day at a DST boundary, and
// for a date before start it rounds the wrong way, so an event that began
// before the grid is placed a day late and stops being recognised as one that
// carried in from earlier.
func daysBetween(from, to time.Time) int {
	return int(to.Sub(from).Round(24*time.Hour) / (24 * time.Hour))
}

// spansDays reports whether an all-day event covers more than one day, and so
// is drawn as a bar across the grid rather than as a chip in one cell.
func spansDays(e ics.Event, loc *time.Location) bool {
	if !e.AllDay {
		return false
	}
	first, last := eventDays(e, loc)
	return last.After(first)
}

// eventsByDay buckets events by local date, and separates out the multi-day
// all-day events that are drawn as spanning bars instead.
//
// Keyed by full date rather than day-of-month: a rolling window routinely
// spans two months, and keying on the day number alone would put the 3rd of
// August on the 3rd of July.
//
// A spanning event is deliberately absent from the per-day buckets. It is
// drawn once, across the days it covers, so leaving it in would also draw it
// as a chip on its first day — the same event twice, in two different shapes.
func (w *Calendar) eventsByDay(ctx Context) (map[string][]ics.Event, []ics.Event, Staleness, bool) {
	r := store.Get[source.CalendarData](ctx.Data, source.KeyCalendar)
	st := StalenessOf(r, ctx.Now)
	data, ok := r.Get()
	if !ok {
		return nil, nil, st, false
	}
	loc := ctx.Location()

	out := map[string][]ics.Event{}
	var spans []ics.Event
	for _, e := range data.Events {
		if !matchesFeeds(e.FeedID, w.cfg.Feeds) {
			continue
		}
		if spansDays(e, loc) {
			if w.cfg.ShowEvents {
				spans = append(spans, e)
				continue
			}
			// Dots mode has no band to hang a bar in, so the event marks
			// every day it covers instead. Marking only its first day would
			// leave the rest of a week away looking free.
			first, last := eventDays(e, loc)
			for d, n := first, 0; !d.After(last) && n < maxSpanDays; d, n = d.AddDate(0, 0, 1), n+1 {
				key := d.Format("2006-01-02")
				out[key] = append(out[key], e)
			}
			continue
		}
		key := dayOf(e.Start, loc).Format("2006-01-02")
		out[key] = append(out[key], e)
	}
	return out, spans, st, true
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
	byDay, spans, st, ok := w.eventsByDay(ctx)
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

	// Spans are keyed on both ends. They are not in byDay, and a bar whose
	// end date moved is the same title on the same start day — so keying on
	// the start alone would leave the old length on screen until something
	// else happened to change.
	loc := ctx.Location()
	for _, e := range spans {
		first, last := eventDays(e, loc)
		fmt.Fprintf(&b, "|s:%s%s-%s", e.Summary,
			first.Format("0102"), last.Format("0102"))
	}
	return b.String()
}

func (w *Calendar) Render(dst *image.RGBA, bounds image.Rectangle, ctx Context) {
	byDay, spans, st, _ := w.eventsByDay(ctx)
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

	// One face for the whole grid, sized from the space a cell has once the
	// date number is drawn, and shared by the bars and the chips. Letting each
	// cell size its own text would set the bars and the chips beneath them at
	// different sizes as soon as the band ate into a cell's remaining height.
	visible := func(d time.Time) bool {
		return w.cfg.Mode != "month" || d.Month() == now.Month()
	}
	cellBodyH := rowH - pad - dayFace.Height() - 2
	evFace, err := ctx.Fonts.Face(render.Regular, clampInt(cellBodyH/4, 9, 18))
	if err != nil {
		return
	}
	lineH := evFace.Height() + 2

	// Spans take at most half the cell body, and never the last line: a day
	// covered by four bars still has to be able to say what else is on it.
	plan := spanPlan{hidden: map[string]int{}}
	if w.cfg.ShowEvents && len(spans) > 0 && lineH > 0 {
		maxLanes := clampInt(cellBodyH/lineH-1, 0, 4)
		if maxLanes > 0 {
			plan = planSpans(spans, start, rows, maxLanes, ctx.Location(), visible)
		} else {
			// No room for a band at all. Everything spanning is hidden, and
			// each day it covers says so through its own "+N more".
			for _, e := range spans {
				first, last := eventDays(e, ctx.Location())
				for d, n := first, 0; !d.After(last) && n < maxSpanDays; d, n = d.AddDate(0, 0, 1), n+1 {
					if visible(d) {
						plan.hidden[d.Format("2006-01-02")]++
					}
				}
			}
		}
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

		// A cell whose date will not fit draws nothing at all. rowH is the
		// height available divided by the row count, but the date is sized
		// independently, so on a short tile the bottom row's number is taller
		// than its cell and spills past the tile. The compositor clears only
		// this widget's rectangle, so those pixels survive as litter across
		// whatever is drawn below. Half a number is no use anyway.
		if textY+face.Height() > bounds.Max.Y {
			continue
		}
		face.DrawTop(dst, textX, textY, label, fg)

		// Chips start below the band of spanning bars, which is reserved
		// across the whole row so that bars stay aligned with each other.
		bandH := plan.laneCount(key) * lineH
		if w.cfg.ShowEvents && (len(byDay[key]) > 0 || plan.hidden[key] > 0) {
			evArea := image.Rect(
				cell.Min.X+pad, textY+face.Height()+2+bandH,
				cell.Max.X-pad, min(cy+rowH-2, bounds.Max.Y),
			)
			w.drawEvents(dst, byDay[key], evArea, evFace, ctx.Location(), plan.hidden[key])
		} else if len(byDay[key]) > 0 {
			w.drawDots(dst, byDay[key], textX, textY+face.Height()+2, 2)
		}
	}

	// Bars last: they cross cell boundaries, so drawing them inside the cell
	// loop would let a later cell's background paint over the tail of a bar
	// that started in an earlier one. Today's fill in particular covers its
	// whole cell, and a bar crossing today has to survive it.
	w.drawSpans(dst, plan, bounds, y, colW, rowH,
		pad, dayFace.Height(), lineH, evFace)
}

// drawSpans paints the multi-day bars.
//
// Each bar is the calendar's colour with the title knocked out in black or
// white, matching the single-day all-day chip, so the two read as the same
// kind of thing at different lengths. The title repeats on every week row the
// event covers: a bar continuing onto a second row with no words on it is not
// obviously the same event, and on a mirror nobody is going to trace it back.
func (w *Calendar) drawSpans(dst *image.RGBA, plan spanPlan, bounds image.Rectangle,
	gridTop, colW, rowH, pad, numberH, lineH int, face *render.Face) {

	for _, s := range plan.segs {
		top := gridTop + s.row*rowH + pad + numberH + 2 + s.lane*lineH
		bottom := top + lineH - 2
		if bottom > bounds.Max.Y {
			continue
		}

		x0 := bounds.Min.X + s.col0*colW + pad
		x1 := bounds.Min.X + (s.col1+1)*colW - pad
		if x1 <= x0 {
			continue
		}
		bar := image.Rect(x0, top, x1, bottom)

		c, ok := render.ParseHexColor(s.event.Color)
		if !ok {
			c = render.Secondary
		}
		render.FillRounded(dst, bar, 3, c)

		// Square off the end that carries on, so the bar butts against the
		// edge of the week rather than closing there.
		const capW = 3
		if s.fromPrev {
			render.Fill(dst, image.Rect(bar.Min.X, bar.Min.Y, bar.Min.X+capW, bar.Max.Y), c)
		}
		if s.toNext {
			render.Fill(dst, image.Rect(bar.Max.X-capW, bar.Min.Y, bar.Max.X, bar.Max.Y), c)
		}

		label := s.event.Summary
		if s.fromPrev {
			// A leading ellipsis is the cheapest way to say "this started
			// before the row you are reading".
			label = "… " + label
		}
		if t := face.Truncate(label, bar.Dx()-6); t != "" {
			face.DrawTop(dst, bar.Min.X+3, top-1, t, contrastOn(c))
		}
	}
}

// maxSpanDays bounds how far a single event is followed across the calendar.
//
// A feed containing an all-day event that runs for years is not hypothetical —
// "Maternity leave", or a mis-exported task list — and every loop that walks
// an event day by day has to terminate on one.
const maxSpanDays = 400

// spanSeg is one week-row's worth of a multi-day event: the run of columns it
// covers on that row, and which lane it sits in.
type spanSeg struct {
	event ics.Event
	row   int
	col0  int // inclusive
	col1  int // inclusive
	lane  int

	// fromPrev and toNext record that the event continues past this row, so
	// the bar can be squared off at that end rather than rounded. A rounded
	// cap says "ends here"; squaring it is what makes a bar read as one event
	// crossing a week boundary rather than two events in consecutive weeks.
	fromPrev bool
	toNext   bool
}

// spanPlan is the laid-out result for a whole grid.
type spanPlan struct {
	segs []spanSeg

	// reserved is how many lanes each individual day has to skip before its
	// own chips can start, keyed by date.
	//
	// Per day rather than per row. Reserving the row's deepest lane for every
	// cell in it keeps the bars tidy, but it also pushes down the events of
	// days the bar never touches — a Tuesday with nothing spanning it would
	// sit a line lower because of a bar on the Sunday, with a blank gap above
	// it. On a mirror those lines are the scarcest thing on the tile.
	reserved map[string]int

	// hidden counts, per date key, the spanning events that did not fit. They
	// are added to that day's "+N more" — a day inside a hidden week-long
	// event must not look empty.
	hidden map[string]int
}

// laneCount is how many lanes a given day has to skip before its own chips
// start. Zero for a day nothing spans, which is most of them.
func (p spanPlan) laneCount(dateKey string) int { return p.reserved[dateKey] }

// planSpans slices multi-day events into per-row segments and packs them into
// lanes.
//
// Lanes are assigned over the linear day index rather than per row. That is
// what keeps one event on the same lane from one week to the next: assigning
// greedily within each row independently lets a bar jump from the first lane
// to the third as it crosses a week boundary, and the eye reads the jump as
// two unrelated events.
//
// visible reports whether a date has a cell drawn for it at all, which is how
// the month view's blank leading and trailing days are handled — a bar must
// stop at the edge of the month rather than run out over empty cells.
//
// Pure, and separate from drawing, because the wrapping and packing is where
// the mistakes live and none of it needs a framebuffer to check.
func planSpans(events []ics.Event, start time.Time, rows, maxLanes int,
	loc *time.Location, visible func(time.Time) bool) spanPlan {

	plan := spanPlan{hidden: map[string]int{}, reserved: map[string]int{}}
	if rows <= 0 || len(events) == 0 {
		return plan
	}
	lastIdx := rows*7 - 1

	// Index each event's clipped range against the grid.
	type placed struct {
		event    ics.Event
		from, to int // linear day indices, inclusive, already clipped

		// Set when the event carries on past the drawn range — off the edge
		// of the grid, or into a month view's blank cells. Either way the bar
		// is squared at that end for the same reason a week boundary is.
		clipL, clipR bool
	}
	var items []placed

	for _, e := range events {
		first, last := eventDays(e, loc)
		if !last.After(first) {
			// Not a span. Belt and braces: the caller filters these out, but
			// a one-cell bar in place of a chip is a silent wrong answer, so
			// the function that decides what a bar is refuses them too.
			continue
		}
		trueFrom := daysBetween(start, first)
		trueTo := daysBetween(start, last)
		if trueTo < 0 || trueFrom > lastIdx {
			continue
		}
		from, to := max(trueFrom, 0), min(trueTo, lastIdx)

		// Trim to days that actually have a cell. Invisible days only ever
		// occur as a contiguous run at each end of a month grid, so walking
		// inwards from both ends cannot leave a hole in the middle.
		for from <= to && !visible(start.AddDate(0, 0, from)) {
			from++
		}
		for to >= from && !visible(start.AddDate(0, 0, to)) {
			to--
		}
		if from > to {
			continue
		}
		items = append(items, placed{
			event: e, from: from, to: to,
			clipL: from > trueFrom, clipR: to < trueTo,
		})
	}

	// Longest first, then earliest, then by title so the order is stable
	// across frames. Long events taking the top lanes keeps them unbroken;
	// letting a one-week event claim lane 0 ahead of a month-long one leaves
	// the long bar stepping down a lane every week.
	sort.SliceStable(items, func(i, j int) bool {
		li, lj := items[i].to-items[i].from, items[j].to-items[j].from
		if li != lj {
			return li > lj
		}
		if items[i].from != items[j].from {
			return items[i].from < items[j].from
		}
		return items[i].event.Summary < items[j].event.Summary
	})

	// occupied[lane] is the set of day indices already taken in that lane.
	var occupied []map[int]bool

	for _, it := range items {
		lane, fits := -1, false
		for l := range maxLanes {
			if l == len(occupied) {
				occupied = append(occupied, map[int]bool{})
			}
			free := true
			for d := it.from; d <= it.to; d++ {
				if occupied[l][d] {
					free = false
					break
				}
			}
			if free {
				lane, fits = l, true
				break
			}
		}

		if !fits {
			for d := it.from; d <= it.to; d++ {
				plan.hidden[start.AddDate(0, 0, d).Format("2006-01-02")]++
			}
			continue
		}
		for d := it.from; d <= it.to; d++ {
			occupied[lane][d] = true
			key := start.AddDate(0, 0, d).Format("2006-01-02")
			plan.reserved[key] = max(plan.reserved[key], lane+1)
		}

		// Cut the run into one segment per week row.
		for row := it.from / 7; row <= it.to/7; row++ {
			rowFrom, rowTo := max(it.from, row*7), min(it.to, row*7+6)
			plan.segs = append(plan.segs, spanSeg{
				event:    it.event,
				row:      row,
				col0:     rowFrom % 7,
				col1:     rowTo % 7,
				lane:     lane,
				fromPrev: rowFrom > it.from || (rowFrom == it.from && it.clipL),
				toNext:   rowTo < it.to || (rowTo == it.to && it.clipR),
			})
		}
	}
	return plan
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
// extra is an overflow count from elsewhere — spanning events that could not
// be given a lane. It is folded in before the reservation below, so the count
// line is kept for them too rather than only for events dropped here.
//
// Returns the chips to draw and how many events were left over.
func planEvents(events []ics.Event, face *render.Face, width, sq, room int,
	loc *time.Location, extra int) ([]eventChip, int) {

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
	overflow := len(events) - len(chips) + extra
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
// hiddenSpans is added to the overflow count: spanning events that could not
// be given a lane are invisible on this day, and a day inside a week-long
// event that did not fit must not read as an empty one.
func (w *Calendar) drawEvents(dst *image.RGBA, events []ics.Event, area image.Rectangle,
	face *render.Face, loc *time.Location, hiddenSpans int) {

	if area.Dy() < 8 || area.Dx() < 20 {
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
	chips, overflow := planEvents(events, face, area.Dx(), sq, room, loc, hiddenSpans)

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
