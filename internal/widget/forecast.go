package widget

import (
	"encoding/json"
	"fmt"
	"image"
	"strings"

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
		DefaultSpan: layout.Span{Cols: 5, Rows: 4},
		MinSpan:     layout.Span{Cols: 3, Rows: 2},
		Needs:       []SourceKind{SourceForecast},
		Fields: []Field{
			{
				Key: "days", Label: "Days to show", Type: FieldNumber,
				Default: 5, Min: f64(1), Max: f64(5),
			},
			{
				Key: "orientation", Label: "Layout", Type: FieldSelect,
				Default: "horizontal",
				Options: []Option{
					{Value: "horizontal", Label: "Across (columns)"},
					{Value: "vertical", Label: "Down (rows)"},
				},
			},
		},
		New: newForecast,
	})
}

type forecastConfig struct {
	Days        int    `json:"days"`
	Orientation string `json:"orientation"`
}

// Forecast renders the multi-day outlook.
type Forecast struct {
	cfg forecastConfig
}

func newForecast(raw json.RawMessage) (Widget, error) {
	cfg := forecastConfig{Days: 5, Orientation: "horizontal"}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &cfg); err != nil {
			return nil, err
		}
	}
	cfg.Days = clampInt(cfg.Days, 1, 5)
	return &Forecast{cfg: cfg}, nil
}

func (w *Forecast) days(ctx Context) ([]source.ForecastDay, Staleness, bool) {
	r := store.Get[source.Conditions](ctx.Data, source.KeyWeather)
	st := StalenessOf(r, ctx.Now)
	c, ok := r.Get()
	if !ok || len(c.Forecast) == 0 {
		return nil, st, false
	}
	days := c.Forecast
	if len(days) > w.cfg.Days {
		days = days[:w.cfg.Days]
	}
	return days, st, true
}

func (w *Forecast) Key(ctx Context) string {
	days, st, ok := w.days(ctx)
	if !ok {
		return "forecast|none|" + st.Key()
	}
	var b strings.Builder
	b.WriteString("forecast|")
	b.WriteString(st.Key())
	for _, d := range days {
		fmt.Fprintf(&b, "|%s:%d:%.0f:%.0f",
			d.Date.Format("01-02"), d.Code, d.High, d.Low)
	}
	return b.String()
}

func (w *Forecast) Render(dst *image.RGBA, bounds image.Rectangle, ctx Context) {
	days, st, ok := w.days(ctx)

	if !ok {
		w.renderEmpty(dst, bounds, ctx, st)
		return
	}
	st.DrawMarker(dst, bounds, ctx, 14)
	if w.cfg.Orientation == "vertical" {
		w.renderVertical(dst, bounds, ctx, days)
		return
	}
	w.renderHorizontal(dst, bounds, ctx, days)
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

func (w *Forecast) renderHorizontal(dst *image.RGBA, bounds image.Rectangle, ctx Context, days []source.ForecastDay) {
	n := len(days)
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

	iconBox := min(colW*2/3, bounds.Dy()-labelFace.Height()-tempFace.Height()-12)
	iconBox = max(iconBox, 0)

	for i, d := range days {
		cx := bounds.Min.X + i*colW + colW/2
		y := bounds.Min.Y

		label := d.Date.Format("Mon")
		lw := labelFace.Measure(label)
		labelFace.DrawTop(dst, cx-lw/2, y, label, render.Secondary)
		y += labelFace.Height() + 4

		if iconBox > 8 {
			if icon, found := render.Icon(d.Icon(), iconBox, iconBox); found {
				b := icon.Bounds()
				render.DrawImage(dst, icon, image.Pt(cx-b.Dx()/2, y))
			}
			y += iconBox + 4
		}

		metric := ctx.Metric()
		hi := fmt.Sprintf("%.0f°", d.High)
		lo := fmt.Sprintf("%.0f°", d.Low)
		_ = metric

		hw := tempFace.Measure(hi)
		lw2 := tempFace.Measure(lo)
		gap := 6
		total := hw + gap + lw2
		x := cx - total/2

		if y+tempFace.Height() <= bounds.Max.Y {
			x = tempFace.DrawTop(dst, x, y, hi, render.Primary)
			tempFace.DrawTop(dst, x+gap, y, lo, render.Muted)
		}
	}
}

func (w *Forecast) renderVertical(dst *image.RGBA, bounds image.Rectangle, ctx Context, days []source.ForecastDay) {
	n := len(days)
	rowH := bounds.Dy() / n
	size := clampInt(rowH/2, 12, 28)

	face, err := ctx.Fonts.Face(render.Regular, size)
	if err != nil {
		return
	}
	iconBox := min(rowH-6, size*2)

	for i, d := range days {
		y := bounds.Min.Y + i*rowH
		x := bounds.Min.X

		face.DrawTop(dst, x, y+(rowH-face.Height())/2, d.Date.Format("Mon"), render.Secondary)
		x += face.Measure("Wed") + 12

		if icon, found := render.Icon(d.Icon(), iconBox, iconBox); found {
			render.DrawImage(dst, icon, image.Pt(x, y+(rowH-icon.Bounds().Dy())/2))
		}

		temps := fmt.Sprintf("%.0f° / %.0f°", d.High, d.Low)
		tw := face.Measure(temps)
		face.DrawTop(dst, bounds.Max.X-tw, y+(rowH-face.Height())/2, temps, render.Primary)
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
