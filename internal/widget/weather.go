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
		Type:        "weather",
		Name:        "Current Weather",
		Description: "Temperature, conditions, wind and sun times for the configured location.",
		DefaultSpan: layout.Span{Cols: 5, Rows: 3},
		MinSpan:     layout.Span{Cols: 3, Rows: 2},
		Needs:       []SourceKind{SourceWeather},
		Fields: []Field{
			{Key: "showFeelsLike", Label: "Show \"feels like\"", Type: FieldBool, Default: true},
			{Key: "showWind", Label: "Show wind", Type: FieldBool, Default: true},
			{Key: "showSun", Label: "Show sunrise/sunset", Type: FieldBool, Default: true},
			{Key: "showLocation", Label: "Show location name", Type: FieldBool, Default: true},
			{
				Key: "tempSize", Label: "Temperature size (px)", Type: FieldNumber,
				Default: 88, Min: f64(24), Max: f64(280),
			},
		},
		New: newWeather,
	})
}

type weatherConfig struct {
	ShowFeelsLike bool `json:"showFeelsLike"`
	ShowWind      bool `json:"showWind"`
	ShowSun       bool `json:"showSun"`
	ShowLocation  bool `json:"showLocation"`
	TempSize      int  `json:"tempSize"`
}

// Weather renders current conditions.
//
// Ported from v1's src/modules/widgets/weather_widget.cpp — minus its
// constructor defaults, which invented a complete Dallas forecast whenever
// the fetch failed.
type Weather struct {
	cfg weatherConfig
}

func newWeather(raw json.RawMessage) (Widget, error) {
	cfg := weatherConfig{
		ShowFeelsLike: true,
		ShowWind:      true,
		ShowSun:       true,
		ShowLocation:  true,
		TempSize:      88,
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &cfg); err != nil {
			return nil, err
		}
	}
	if cfg.TempSize < 24 {
		cfg.TempSize = 88
	}
	return &Weather{cfg: cfg}, nil
}

func (w *Weather) reading(ctx Context) (source.Conditions, Staleness, bool) {
	r := store.Get[source.Conditions](ctx.Data, source.KeyWeather)
	st := StalenessOf(r, ctx.Now)
	v, ok := r.Get()
	return v, st, ok
}

func (w *Weather) Key(ctx Context) string {
	c, st, ok := w.reading(ctx)
	if !ok {
		return "weather|none|" + st.Key()
	}
	return fmt.Sprintf("weather|%.0f|%.0f|%d|%.0f|%d|%s|%s|%s",
		c.Temperature, c.FeelsLike, c.Humidity, c.WindSpeed, c.Code,
		c.City, st.Key(), degreeUnit(c.Metric))
}

func (w *Weather) Render(dst *image.RGBA, bounds image.Rectangle, ctx Context) {
	cond, st, ok := w.reading(ctx)

	tempFace, err := ctx.Fonts.Face(render.Light, w.cfg.TempSize)
	if err != nil {
		return
	}
	subSize := max(14, w.cfg.TempSize/4)
	subFace, err := ctx.Fonts.Face(render.Regular, subSize)
	if err != nil {
		return
	}
	smallFace, err := ctx.Fonts.Face(render.Light, max(12, subSize*3/4))
	if err != nil {
		return
	}

	st.DrawMarker(dst, bounds, ctx, max(12, subSize*3/4))

	x, y := bounds.Min.X, bounds.Min.Y

	// Location line.
	if w.cfg.ShowLocation {
		place := Placeholder
		if ok && cond.City != "" {
			place = cond.City
			if cond.Region != "" {
				place += ", " + cond.Region
			}
		}
		subFace.DrawTop(dst, x, y, subFace.Truncate(place, bounds.Dx()*2/3), render.Secondary)
		y += subFace.Height() + 4
	}

	// Temperature, with the icon to its right.
	temp := Placeholder
	if ok {
		temp = fmt.Sprintf("%.0f%s", cond.Temperature, degreeUnit(cond.Metric))
	}
	tempEnd := tempFace.DrawTop(dst, x, y, temp, render.Primary)

	if ok {
		iconSize := w.cfg.TempSize
		if icon, found := render.Icon(cond.Icon(), iconSize, iconSize); found {
			pt := image.Pt(tempEnd+subSize/2, y+(tempFace.Height()-icon.Bounds().Dy())/2)
			if pt.X+icon.Bounds().Dx() <= bounds.Max.X {
				render.DrawImage(dst, icon, pt)
			}
		}
	}
	y += tempFace.Height()

	// Condition description.
	desc := Placeholder
	if ok {
		desc = cond.Condition()
	}
	subFace.DrawTop(dst, x, y, subFace.Truncate(desc, bounds.Dx()), render.Secondary)
	y += subFace.Height() + 6

	// Detail lines. Each is omitted entirely rather than shown with a
	// placeholder value — a wind speed of "—" tells you nothing a missing
	// line does not.
	var details []string
	if ok {
		if w.cfg.ShowFeelsLike {
			details = append(details,
				fmt.Sprintf("Feels %.0f%s", cond.FeelsLike, degreeUnit(cond.Metric)))
		}
		if w.cfg.ShowWind {
			details = append(details,
				fmt.Sprintf("Wind %.0f %s %s", cond.WindSpeed, speedUnit(cond.Metric),
					compass(cond.WindDirection)))
		}
		if w.cfg.ShowSun && !cond.Sunset.IsZero() {
			sun := "Sunset " + cond.Sunset.In(ctx.Location()).Format("3:04pm")
			if ctx.Now.Before(cond.Sunrise) && !cond.Sunrise.IsZero() {
				sun = "Sunrise " + cond.Sunrise.In(ctx.Location()).Format("3:04pm")
			}
			details = append(details, sun)
		}
	}

	for _, line := range details {
		if y+smallFace.Height() > bounds.Max.Y {
			break
		}
		smallFace.DrawTop(dst, x, y, smallFace.Truncate(line, bounds.Dx()), render.Muted)
		y += smallFace.Height() + 2
	}

	// When there is genuinely nothing, say why rather than leaving a void.
	if !ok && st.Err != nil && y+smallFace.Height() <= bounds.Max.Y {
		msg := firstLine(st.Err.Error())
		smallFace.DrawTop(dst, x, y, smallFace.Truncate(msg, bounds.Dx()), render.Faint)
	}
}

func degreeUnit(metric bool) string {
	if metric {
		return "°C"
	}
	return "°F"
}

func speedUnit(metric bool) string {
	if metric {
		return "km/h"
	}
	return "mph"
}

// compass converts a bearing in degrees to a 16-point compass label.
func compass(deg int) string {
	points := []string{
		"N", "NNE", "NE", "ENE", "E", "ESE", "SE", "SSE",
		"S", "SSW", "SW", "WSW", "W", "WNW", "NW", "NNW",
	}
	idx := int((float64(deg)+11.25)/22.5) % len(points)
	if idx < 0 {
		idx += len(points)
	}
	return points[idx]
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
