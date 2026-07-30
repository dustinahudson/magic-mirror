package widget

import (
	"encoding/json"
	"image"
	"strings"

	"github.com/dustinahudson/magic-mirror/internal/layout"
	"github.com/dustinahudson/magic-mirror/internal/render"
)

func init() {
	Register(Descriptor{
		Type:        "datetime",
		Name:        "Clock",
		Description: "Current time and date.",
		DefaultSpan: layout.Span{Cols: 4, Rows: 3},
		MinSpan:     layout.Span{Cols: 2, Rows: 2},
		Fields: []Field{
			{Key: "format24h", Label: "24-hour clock", Type: FieldBool, Default: false},
			{Key: "showSeconds", Label: "Show seconds", Type: FieldBool, Default: true},
			{Key: "showDate", Label: "Show date", Type: FieldBool, Default: true},
			{
				Key: "dateFormat", Label: "Date format", Type: FieldSelect,
				Default: "long",
				Options: []Option{
					{Value: "long", Label: "Monday, January 2"},
					{Value: "medium", Label: "Mon, Jan 2"},
					{Value: "short", Label: "01/02/2006"},
				},
			},
			{
				Key: "timeSize", Label: "Time size (px)", Type: FieldNumber,
				Default: 96, Min: f64(24), Max: f64(320),
				Help: "Height of the clock digits. Everything else scales from this.",
			},
		},
		New: newDateTime,
	})
}

func f64(v float64) *float64 { return new(v) }

// maxString returns whichever of a or b is longer, for use as a width
// sample when either might be the wider of the two.
func maxString(a, b string) string {
	if len(b) > len(a) {
		return b
	}
	return a
}

type dateTimeConfig struct {
	Format24h   bool   `json:"format24h"`
	ShowSeconds bool   `json:"showSeconds"`
	ShowDate    bool   `json:"showDate"`
	DateFormat  string `json:"dateFormat"`
	TimeSize    int    `json:"timeSize"`
}

// DateTime renders the clock. Ported from v1's
// src/modules/widgets/datetime_widget.cpp, which stacked a grey date above a
// white time with seconds and am/pm hung off its right edge.
type DateTime struct {
	cfg dateTimeConfig
}

func newDateTime(raw json.RawMessage) (Widget, error) {
	cfg := dateTimeConfig{
		ShowSeconds: true,
		ShowDate:    true,
		DateFormat:  "long",
		TimeSize:    96,
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &cfg); err != nil {
			return nil, err
		}
	}
	if cfg.TimeSize < 24 {
		cfg.TimeSize = 96
	}
	return &DateTime{cfg: cfg}, nil
}

func (w *DateTime) parts(ctx Context) (date, clock, secs, ampm string) {
	now := ctx.Local()

	if w.cfg.Format24h {
		clock = now.Format("15:04")
	} else {
		clock = now.Format("3:04")
		ampm = strings.ToLower(now.Format("PM"))
	}
	if w.cfg.ShowSeconds {
		secs = now.Format("05")
	}
	if w.cfg.ShowDate {
		switch w.cfg.DateFormat {
		case "medium":
			date = now.Format("Mon, Jan 2")
		case "short":
			date = now.Format("01/02/2006")
		default:
			date = now.Format("Monday, January 2")
		}
	}
	return
}

func (w *DateTime) Key(ctx Context) string {
	d, c, s, a := w.parts(ctx)
	return d + "|" + c + "|" + s + "|" + a
}

func (w *DateTime) Render(dst *image.RGBA, bounds image.Rectangle, ctx Context) {
	date, clock, secs, ampm := w.parts(ctx)

	// Fit the clock to the tile rather than trusting the configured size.
	//
	// timeSize is a preference: a 96px clock in a tile two rows tall has to
	// give way, and clipping the digits is the worst possible resolution.
	// The date line and the seconds/am-pm column both take space from the
	// clock, so they are budgeted for before the fit.
	avail := bounds
	dateH := 0
	if date != "" {
		// The date gets at most a quarter of the tile.
		if f, err := ctx.Fonts.FitFace(render.Regular,
			max(12, w.cfg.TimeSize/3), date, bounds.Dx(), bounds.Dy()/4); err == nil {
			dateH = f.Height() + f.Size()/4
		}
	}
	avail.Min.Y += dateH

	// Reserve room for the seconds/am-pm column beside the digits.
	sample := clock
	if secs != "" || ampm != "" {
		sample = clock + "  " + maxString(secs, ampm)
	}

	timeFace, err := ctx.Fonts.FitFace(render.Light, w.cfg.TimeSize,
		sample, avail.Dx(), avail.Dy())
	if err != nil {
		return
	}
	size := timeFace.Size()

	// The satellites sit at roughly a third of the clock height, matching the
	// 48/24 ratio v1 used.
	subSize := max(10, size/3)
	subFace, err := ctx.Fonts.Face(render.Regular, subSize)
	if err != nil {
		return
	}

	x, y := bounds.Min.X, bounds.Min.Y

	if date != "" {
		dateFace, err := ctx.Fonts.FitFace(render.Regular,
			max(12, w.cfg.TimeSize/3), date, bounds.Dx(), bounds.Dy()/4)
		if err == nil {
			dateFace.DrawTop(dst, x, y, dateFace.Truncate(date, bounds.Dx()), render.Secondary)
			y += dateFace.Height() + dateFace.Size()/4
		}
	}

	end := timeFace.DrawTop(dst, x, y, clock, render.Primary)

	// Seconds ride the top of the digits, am/pm the baseline — the offset
	// arithmetic v1 did with LV_ALIGN_OUT_RIGHT_TOP / _BOTTOM.
	if secs != "" || ampm != "" {
		gap := max(4, size/16)
		top := y + timeFace.Ascent() - subFace.Ascent() - (timeFace.Ascent()-subFace.Ascent())/2
		if secs != "" {
			subFace.DrawTop(dst, end+gap, top, secs, render.Primary)
		}
		if ampm != "" {
			base := y + timeFace.Ascent() - subFace.Ascent()
			if secs != "" {
				base = top + subFace.Height()
			}
			subFace.DrawTop(dst, end+gap, base, ampm, render.Secondary)
		}
	}
}
