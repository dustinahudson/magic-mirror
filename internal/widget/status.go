package widget

import (
	"encoding/json"
	"fmt"
	"image"
	"sort"
	"strings"

	"github.com/dustinahudson/magic-mirror/internal/layout"
	"github.com/dustinahudson/magic-mirror/internal/render"
	"github.com/dustinahudson/magic-mirror/internal/source"
	"github.com/dustinahudson/magic-mirror/internal/store"
)

func init() {
	Register(Descriptor{
		Type:        "status",
		Name:        "Status Bar",
		Description: "IP address, version, and the health of every data source.",
		DefaultSpan: layout.Span{Cols: 12, Rows: 1},
		MinSpan:     layout.Span{Cols: 4, Rows: 1},
		Needs:       []SourceKind{SourceSystem},
		Fields: []Field{
			{Key: "showIP", Label: "Show IP address", Type: FieldBool, Default: true},
			{Key: "showVersion", Label: "Show version", Type: FieldBool, Default: true},
			{
				Key: "showSources", Label: "Show source health", Type: FieldBool, Default: true,
				Help: "Marks any source that is stale or failing. Healthy sources stay quiet.",
			},
			{
				Key: "size", Label: "Text size (px)", Type: FieldNumber,
				Default: 14, Min: f64(9), Max: f64(32),
			},
		},
		New: newStatus,
	})
}

type statusConfig struct {
	ShowIP      bool `json:"showIP"`
	ShowVersion bool `json:"showVersion"`
	ShowSources bool `json:"showSources"`
	Size        int  `json:"size"`
}

// Status is the bottom bar.
//
// v1 packed this into a single lv_label_set_text_fmt (kernel.cpp:474) that
// always showed the same fields. Here, healthy sources say nothing and only
// problems earn space — a status bar that is always full teaches you to stop
// reading it.
type Status struct {
	cfg statusConfig
}

func newStatus(raw json.RawMessage) (Widget, error) {
	cfg := statusConfig{ShowIP: true, ShowVersion: true, ShowSources: true, Size: 14}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &cfg); err != nil {
			return nil, err
		}
	}
	cfg.Size = clampInt(cfg.Size, 9, 32)
	return &Status{cfg: cfg}, nil
}

// segment is one piece of the status bar.
type segment struct {
	text  string
	color render.RGBA
}

func (w *Status) segments(ctx Context) []segment {
	var out []segment

	sys, sysOK := store.Get[source.SystemInfo](ctx.Data, source.KeySystem).Get()

	if w.cfg.ShowIP {
		switch {
		case sysOK && sys.IP != "":
			out = append(out, segment{sys.IP, render.Secondary})
		case sysOK:
			out = append(out, segment{"no network", render.Error})
		default:
			out = append(out, segment{"starting…", render.Muted})
		}
	}

	if w.cfg.ShowSources {
		out = append(out, w.sourceSegments(ctx)...)
	}

	if w.cfg.ShowVersion && sysOK && sys.Version != "" {
		out = append(out, segment{sys.Version, render.Faint})
	}
	return out
}

// sourceSegments reports only unhealthy sources.
func (w *Status) sourceSegments(ctx Context) []segment {
	keys := ctx.Data.Keys()
	sort.Strings(keys)

	var out []segment
	for _, key := range keys {
		if key == source.KeySystem {
			continue
		}
		e, ok := ctx.Data.Entry(key)
		if !ok || e.Status == store.Fresh {
			continue
		}

		st := Staleness{Status: e.Status, Age: e.Age(ctx.Now), Err: e.LastErr}
		out = append(out, segment{key + " " + st.Label(), st.Color()})
	}

	// Calendar feeds can fail individually while the source as a whole
	// succeeds, so surface that separately rather than letting a broken feed
	// hide behind a green source.
	if data, ok := store.Get[source.CalendarData](ctx.Data, source.KeyCalendar).Get(); ok {
		if n := len(data.FeedErrors); n > 0 {
			out = append(out, segment{
				fmt.Sprintf("%d calendar%s failing", n, plural(n)),
				render.Warn,
			})
		}
		if data.Truncated {
			out = append(out, segment{"event list truncated", render.Warn})
		}
	}
	return out
}

func (w *Status) Key(ctx Context) string {
	segs := w.segments(ctx)
	parts := make([]string, len(segs))
	for i, s := range segs {
		parts[i] = s.text
	}
	return "status|" + strings.Join(parts, "|")
}

func (w *Status) Render(dst *image.RGBA, bounds image.Rectangle, ctx Context) {
	// The status bar is usually a single grid row, and a configured size
	// larger than that row would simply be clipped by the screen edge.
	face, err := ctx.Fonts.FitFace(render.Light, w.cfg.Size, "", 0, bounds.Dy())
	if err != nil {
		return
	}

	segs := w.segments(ctx)
	if len(segs) == 0 {
		return
	}

	y := bounds.Min.Y + (bounds.Dy()-face.Height())/2
	x := bounds.Min.X
	sep := "  ·  "
	sepW := face.Measure(sep)

	for i, s := range segs {
		if i > 0 {
			if x+sepW > bounds.Max.X {
				break
			}
			x = face.DrawTop(dst, x, y, sep, render.Faint)
		}
		remaining := bounds.Max.X - x
		if remaining <= 0 {
			break
		}
		x = face.DrawTop(dst, x, y, face.Truncate(s.text, remaining), s.color)
	}
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
