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
			{Key: "showMoon", Label: "Show moon phase", Type: FieldBool, Default: true},
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
	ShowMoon      bool `json:"showMoon"`
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
		ShowMoon:      true,
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
	return fmt.Sprintf("weather|%.0f|%.0f|%d|%.0f|%d|%d|%s|%s|%s|%s|%d",
		c.Temperature, c.FeelsLike, c.Humidity, c.WindSpeed, c.WindDirection, c.Code,
		c.City, st.Key(), degreeUnit(c.Metric),
		c.Sunrise.Format("15:04")+c.Sunset.Format("15:04"), int(c.Moon))
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

	// Wind, sun times and moon phase, on one row above the temperature —
	// the arrangement v1 used (a flex row of wind, sunrise, and the moon
	// beside sunset, all at one size).
	//
	// Drawn before the temperature so it gets its space first: the
	// temperature is the one element that can shrink without losing
	// information, since the number stays legible at any size.
	if ok {
		y = w.drawDetailRow(dst, image.Rect(x, y, bounds.Max.X, bounds.Max.Y), ctx, cond, smallFace)
	}

	// Temperature, with the icon to its right. Refit to whatever vertical
	// space the detail row left.
	temp := Placeholder
	if ok {
		temp = fmt.Sprintf("%.0f%s", cond.Temperature, degreeUnit(cond.Metric))
	}
	if f, err := ctx.Fonts.FitFace(render.Light, w.cfg.TempSize,
		temp+"  ", bounds.Dx()*3/4, bounds.Max.Y-y); err == nil {
		tempFace = f
	}

	tempEnd := tempFace.DrawTop(dst, x, y, temp, render.Primary)
	right := tempEnd

	if ok {
		iconSize := min(tempFace.Size(), bounds.Max.Y-y)
		if icon, found := render.Icon(cond.Icon(), iconSize, iconSize); found {
			pt := image.Pt(tempEnd+subSize/2, y+(tempFace.Height()-icon.Bounds().Dy())/2)
			if pt.X+icon.Bounds().Dx() <= bounds.Max.X {
				render.DrawImage(dst, icon, pt)
				right = pt.X + icon.Bounds().Dx()
			}
		}
	}

	// Condition and "feels like".
	//
	// v1 put these on their own line under the temperature. That works only
	// while the tile is tall: the temperature is the greedy element — FitFace
	// grows it into every pixel offered — so in a two-row tile it took the
	// lot and the line below was silently dropped. "Feels like" simply never
	// appeared, which is the wrong thing to lose, since on a hot or windy day
	// it is the number that changes what you put on.
	//
	// So they go beside the icon, into the empty right half the temperature
	// row already has, and the tile's height stops deciding whether they
	// exist. The line below remains the fallback for a tile too narrow for a
	// column there.
	desc := []string{Placeholder}
	if ok {
		desc = []string{cond.Condition()}
		if w.cfg.ShowFeelsLike {
			desc = append(desc,
				fmt.Sprintf("feels %.0f%s", cond.FeelsLike, degreeUnit(cond.Metric)))
		}
	}

	descW := 0
	for _, line := range desc {
		descW = max(descW, subFace.Measure(line))
	}
	descX := right + subSize/2

	if descX+descW <= bounds.Max.X && len(desc)*subFace.Height() <= tempFace.Height() {
		// Centred against the number rather than top-aligned: the two lines
		// are a caption to it, and a caption hanging off the cap height reads
		// as a separate row.
		dy := y + (tempFace.Height()-len(desc)*subFace.Height())/2
		for _, line := range desc {
			subFace.DrawTop(dst, descX, dy, line, render.Secondary)
			dy += subFace.Height()
		}
		y += tempFace.Height()
	} else {
		y += tempFace.Height()
		joined := strings.Join(desc, "   ")
		if y+subFace.Height() <= bounds.Max.Y {
			subFace.DrawTop(dst, x, y, subFace.Truncate(joined, bounds.Dx()), render.Secondary)
			y += subFace.Height()
		}
	}

	// When there is genuinely nothing, say why rather than leaving a void.
	if !ok && st.Err != nil && y+smallFace.Height() <= bounds.Max.Y {
		msg := firstLine(st.Err.Error())
		smallFace.DrawTop(dst, x, y, smallFace.Truncate(msg, bounds.Dx()), render.Faint)
	}
}

// drawDetailRow renders wind, sunrise/sunset and the moon phase on one line,
// returning the y below it.
//
// v1 drew each with its own icon; only the moon artwork survives as a PNG,
// so wind and the sun times are labelled with arrows instead. The moon keeps
// its icon because the phase is inherently a picture — "Waxing Gibbous" in
// words is a poor substitute for the shape.
func (w *Weather) drawDetailRow(dst *image.RGBA, area image.Rectangle, ctx Context, cond source.Conditions, face *render.Face) int {
	type part struct {
		text string
		icon string
	}
	var parts []part

	if w.cfg.ShowWind {
		parts = append(parts, part{text: fmt.Sprintf("%s %.0f %s",
			compass(cond.WindDirection), cond.WindSpeed, speedUnit(cond.Metric))})
	}
	if w.cfg.ShowSun {
		loc := ctx.Location()
		if !cond.Sunrise.IsZero() {
			parts = append(parts, part{text: "↑ " + strings.ToLower(cond.Sunrise.In(loc).Format("3:04pm"))})
		}
		if !cond.Sunset.IsZero() {
			parts = append(parts, part{text: "↓ " + strings.ToLower(cond.Sunset.In(loc).Format("3:04pm"))})
		}
	}
	if w.cfg.ShowMoon && !cond.Sunrise.IsZero() {
		parts = append(parts, part{text: cond.Moon.Name(), icon: cond.Moon.Icon()})
	}

	if len(parts) == 0 || area.Dy() < face.Height() {
		return area.Min.Y
	}

	iconBox := face.Height()
	gap := face.Size()
	x, y := area.Min.X, area.Min.Y

	for i, p := range parts {
		if i > 0 {
			x += gap
		}
		if p.icon != "" {
			if icon, found := render.Icon(p.icon, iconBox, iconBox); found {
				if x+icon.Bounds().Dx() > area.Max.X {
					break
				}
				render.DrawImage(dst, icon, image.Pt(x, y))
				x += icon.Bounds().Dx() + 4
			}
		}
		if x+face.Measure(p.text) > area.Max.X {
			break
		}
		x = face.DrawTop(dst, x, y, p.text, render.Muted)
	}

	return y + face.Height() + 4
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
