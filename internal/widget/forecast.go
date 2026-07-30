package widget

import (
	"encoding/json"
	"fmt"
	"image"
	"strings"
	"time"

	"github.com/dustinahudson/magic-mirror/internal/layout"
	"github.com/dustinahudson/magic-mirror/internal/render"
	"github.com/dustinahudson/magic-mirror/internal/source"
	"github.com/dustinahudson/magic-mirror/internal/store"
)

func init() {
	Register(Descriptor{
		Type:        "forecast",
		Name:        "Forecast",
		Description: "Multi-day outlook with icons and high/low temperatures.",
		DefaultSpan: layout.Span{Cols: 5, Rows: 5},
		MinSpan:     layout.Span{Cols: 3, Rows: 2},
		Needs:       []SourceKind{SourceForecast},
		Fields: []Field{
			{
				Key: "days", Label: "Days to show", Type: FieldNumber,
				Default: 5, Min: f64(1), Max: f64(6),
			},
			{
				Key: "orientation", Label: "Layout", Type: FieldSelect,
				Default: "vertical",
				Options: []Option{
					{Value: "vertical", Label: "Down — one row per day"},
					{Value: "horizontal", Label: "Across — one column per day"},
				},
				Help: "Down matches the original mirror layout.",
			},
			{
				Key: "includeToday", Label: "Include today", Type: FieldBool,
				Default: true,
				Help:    "Shows today as the first row, labelled \"Today\".",
			},
		},
		New: newForecast,
	})
}

type forecastConfig struct {
	Days         int    `json:"days"`
	Orientation  string `json:"orientation"`
	IncludeToday bool   `json:"includeToday"`
}

// Forecast renders the multi-day outlook.
//
// Ported from the forecast section of v1's weather_widget.cpp: a flex
// column of day rows — day name on the left at a fixed width, icon in the
// middle, high/low on the right. That vertical arrangement is the default
// here for the same reason it was chosen there: a mirror is a tall narrow
// slice of wall, and stacked rows read better across a room than five thin
// columns.
type Forecast struct {
	cfg forecastConfig
}

func newForecast(raw json.RawMessage) (Widget, error) {
	cfg := forecastConfig{
		Days:         5,
		Orientation:  "vertical",
		IncludeToday: true,
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &cfg); err != nil {
			return nil, err
		}
	}
	cfg.Days = clampInt(cfg.Days, 1, 6)
	return &Forecast{cfg: cfg}, nil
}

// days returns the outlook, and the offset of its first entry from today.
//
// Conditions.Forecast starts at today with today's real high and low, so
// including today is a matter of where to start slicing rather than
// synthesising a row. An earlier version built today from the current
// temperature and printed "82° / 82°" — a fabricated range presented as a
// measured one, which is exactly what this design refuses to do elsewhere.
func (w *Forecast) days(ctx Context) ([]source.ForecastDay, int, Staleness, bool) {
	r := store.Get[source.Conditions](ctx.Data, source.KeyWeather)
	st := StalenessOf(r, ctx.Now)
	c, ok := r.Get()
	if !ok || len(c.Forecast) == 0 {
		return nil, 0, st, false
	}

	out := c.Forecast
	offset := 0
	if !w.cfg.IncludeToday {
		if len(out) < 2 {
			return nil, 0, st, false
		}
		out = out[1:]
		offset = 1
	}

	if len(out) > w.cfg.Days {
		out = out[:w.cfg.Days]
	}
	return out, offset, st, true
}

func (w *Forecast) Key(ctx Context) string {
	days, off, st, ok := w.days(ctx)
	if !ok {
		return "forecast|none|" + st.Key()
	}
	var b strings.Builder
	fmt.Fprintf(&b, "forecast|%s|%d", st.Key(), off)
	for _, d := range days {
		fmt.Fprintf(&b, "|%s:%d:%.0f:%.0f", d.Date.Format("01-02"), d.Code, d.High, d.Low)
	}
	return b.String()
}

func (w *Forecast) Render(dst *image.RGBA, bounds image.Rectangle, ctx Context) {
	days, offset, st, ok := w.days(ctx)
	if !ok {
		w.renderEmpty(dst, bounds, ctx, st)
		return
	}

	// No section heading: the compositor rules a line between stacked
	// widgets, which separates the outlook from current conditions without
	// spending a row on a label that says what the icons already say.
	area := bounds
	st.DrawMarker(dst, bounds, ctx, 13)

	if w.cfg.Orientation == "horizontal" {
		w.renderHorizontal(dst, area, ctx, days, offset)
		return
	}
	w.renderVertical(dst, area, ctx, days, offset)
}

func (w *Forecast) renderEmpty(dst *image.RGBA, bounds image.Rectangle, ctx Context, st Staleness) {
	face, err := ctx.Fonts.Face(render.Light, 18)
	if err != nil {
		return
	}
	msg := "No forecast yet"
	if st.Err != nil {
		msg = firstLine(st.Err.Error())
	}
	face.DrawTop(dst, bounds.Min.X, bounds.Min.Y+bounds.Dy()/2,
		face.Truncate(msg, bounds.Dx()), render.Faint)
}

// renderVertical is the v1 arrangement: one row per day, day name left at a
// fixed width, icon centred, high/low right.
func (w *Forecast) renderVertical(dst *image.RGBA, bounds image.Rectangle, ctx Context, days []source.ForecastDay, offset int) {
	n := len(days)
	if n == 0 {
		return
	}
	// Drop rows that will not fit rather than drawing past the tile. An
	// earlier version divided the height by the requested day count
	// regardless, so a five-day forecast in a short tile painted its last
	// row below the boundary and over whatever was beneath.
	rowH := bounds.Dy() / n
	const minRow = 18
	if rowH < minRow {
		n = max(1, bounds.Dy()/minRow)
		days = days[:min(len(days), n)]
		rowH = bounds.Dy() / n
	}

	// The day column and the temperatures both have to fit alongside the
	// icon, so size from the widest thing actually drawn.
	widest := "Tomorrow  88° / 64°"
	face, err := ctx.Fonts.FitFace(render.Regular, clampInt(rowH/2, 12, 34),
		widest, bounds.Dx(), rowH)
	if err != nil {
		return
	}
	size := face.Size()

	// v1 fixed the day column at 120px so "Tomorrow" fit at 22pt. Measuring
	// the widest label we will actually draw does the same job at any size,
	// and stays correct if the labels change.
	dayCol := face.Measure("Tomorrow") + size/2

	iconBox := min(rowH-4, size*2)

	for i, d := range days {
		y := bounds.Min.Y + i*rowH
		if y+rowH > bounds.Max.Y {
			break
		}
		mid := y + (rowH-face.Height())/2

		face.DrawTop(dst, bounds.Min.X, mid, relativeDayName(d.Date, ctx.Local(), i+offset), render.Secondary)

		// Icon sits just past the day column, matching v1's centre slot.
		if icon, found := render.Icon(d.Icon(), iconBox, iconBox); found {
			ix := bounds.Min.X + dayCol
			iy := y + (rowH-icon.Bounds().Dy())/2
			render.DrawImage(dst, icon, image.Pt(ix, iy))
		}

		temps := fmt.Sprintf("%.0f° / %.0f°", d.High, d.Low)
		tw := face.Measure(temps)
		face.DrawTop(dst, bounds.Max.X-tw, mid, temps, render.Secondary)
	}
}

func (w *Forecast) renderHorizontal(dst *image.RGBA, bounds image.Rectangle, ctx Context, days []source.ForecastDay, offset int) {
	n := len(days)
	if n == 0 {
		return
	}
	colW := bounds.Dx() / n

	labelSize := clampInt(colW/6, 12, 24)
	tempSize := clampInt(colW/5, 14, 30)

	labelFace, err := ctx.Fonts.Face(render.Regular, labelSize)
	if err != nil {
		return
	}
	tempFace, err := ctx.Fonts.Face(render.Light, tempSize)
	if err != nil {
		return
	}

	iconBox := max(0, min(colW*2/3, bounds.Dy()-labelFace.Height()-tempFace.Height()-12))

	for i, d := range days {
		cx := bounds.Min.X + i*colW + colW/2
		y := bounds.Min.Y

		// Across, there is no room for "Tomorrow"; the weekday is the most
		// information that fits.
		label := d.Date.Format("Mon")
		if i+offset == 0 {
			label = "Today"
		}
		lw := labelFace.Measure(label)
		labelFace.DrawTop(dst, cx-lw/2, y, label, render.Secondary)
		y += labelFace.Height() + 4

		if iconBox > 8 {
			if icon, found := render.Icon(d.Icon(), iconBox, iconBox); found {
				render.DrawImage(dst, icon, image.Pt(cx-icon.Bounds().Dx()/2, y))
			}
			y += iconBox + 4
		}

		hi := fmt.Sprintf("%.0f°", d.High)
		lo := fmt.Sprintf("%.0f°", d.Low)
		gap := 6
		x := cx - (tempFace.Measure(hi)+gap+tempFace.Measure(lo))/2

		if y+tempFace.Height() <= bounds.Max.Y {
			x = tempFace.DrawTop(dst, x, y, hi, render.Primary)
			tempFace.DrawTop(dst, x+gap, y, lo, render.Muted)
		}
	}
}

// relativeDayName reproduces v1's GetDayName: the first two days are named
// rather than dated, which reads far better than "Thu / Fri" when those are
// simply today and tomorrow.
func relativeDayName(date, now time.Time, index int) string {
	switch index {
	case 0:
		return "Today"
	case 1:
		return "Tomorrow"
	default:
		return date.Format("Mon")
	}
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
