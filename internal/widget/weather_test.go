package widget

import (
	"encoding/json"
	"image"
	"testing"
	"time"

	"github.com/dustinahudson/magic-mirror/internal/render"
	"github.com/dustinahudson/magic-mirror/internal/source"
	"github.com/dustinahudson/magic-mirror/internal/store"
)

// A three-column, two-row tile on a 1080p mirror. This size is the point:
// it is what the default layout uses, and it is where the condition line
// used to be pushed off the bottom.
const (
	wxTileW = 480
	wxTileH = 135
)

func weatherCtx(t *testing.T) Context {
	t.Helper()
	now := time.Date(2025, 7, 30, 15, 0, 0, 0, time.UTC)

	s := store.New()
	s.Success(source.KeyWeather, source.Conditions{
		Temperature: 81, FeelsLike: 86, Humidity: 62,
		WindSpeed: 9, WindDirection: 135, Code: 3, IsDay: true,
		Sunrise: now.Add(-9 * time.Hour), Sunset: now.Add(5 * time.Hour),
		City: "Kansas City", Region: "Missouri",
	})

	return Context{
		Now:   now,
		Loc:   time.UTC,
		Fonts: render.NewFontSet(),
		Data:  s.Load(),
		Units: "imperial",
	}
}

// lit counts pixels that differ from the cleared background.
func lit(img *image.RGBA, r image.Rectangle) int {
	n := 0
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			c := img.RGBAAt(x, y)
			if c.R > 8 || c.G > 8 || c.B > 8 {
				n++
			}
		}
	}
	return n
}

func renderWeather(t *testing.T, cfg string, w, h int) (*image.RGBA, image.Rectangle) {
	t.Helper()
	// A margin around the tile catches anything drawn outside its bounds.
	const margin = 20
	dst := image.NewRGBA(image.Rect(0, 0, w+2*margin, h+2*margin))
	bounds := image.Rect(margin, margin, margin+w, margin+h)

	Build("weather", json.RawMessage(cfg)).Render(dst, bounds, weatherCtx(t))
	return dst, bounds
}

// The reported symptom: "feels like" is enabled by default and was nowhere on
// screen, because it sat under a temperature that had grown into every
// remaining pixel of a short tile.
//
// Asserted as "the setting has a visible effect" rather than against a golden
// image, so it keeps holding if the arrangement changes again.
func TestWeatherFeelsLikeIsVisibleInAShortTile(t *testing.T) {
	on, bounds := renderWeather(t, `{"showFeelsLike":true}`, wxTileW, wxTileH)
	off, _ := renderWeather(t, `{"showFeelsLike":false}`, wxTileW, wxTileH)

	onLit, offLit := lit(on, bounds), lit(off, bounds)
	if onLit <= offLit {
		t.Errorf("enabling feels-like changed nothing on screen: %d lit pixels with, %d without",
			onLit, offLit)
	}
}

// Whatever the arrangement, a widget must not draw outside the tile it was
// given — the compositor clears only the tile, so overdraw persists as
// litter over a neighbour.
func TestWeatherStaysInsideItsBounds(t *testing.T) {
	sizes := []struct {
		name string
		w, h int
	}{
		{"short", wxTileW, wxTileH},
		{"narrow", 320, 135},
		{"tall", 640, 405},
		{"tiny", 160, 70},
	}
	for _, s := range sizes {
		t.Run(s.name, func(t *testing.T) {
			dst, bounds := renderWeather(t, `{"showFeelsLike":true}`, s.w, s.h)
			if n := lit(dst, dst.Bounds()) - lit(dst, bounds); n != 0 {
				t.Errorf("%d pixels drawn outside the tile", n)
			}
		})
	}
}

// Every tile big enough to be legible should show the reading itself. A tile
// that renders nothing at all is the failure mode worth catching.
func TestWeatherDrawsSomethingAtEverySize(t *testing.T) {
	for _, s := range []image.Point{{480, 135}, {320, 135}, {640, 405}, {160, 70}} {
		dst, bounds := renderWeather(t, `{}`, s.X, s.Y)
		if lit(dst, bounds) == 0 {
			t.Errorf("%dx%d tile rendered blank", s.X, s.Y)
		}
	}
}
