package mirror_test

import (
	"encoding/json"
	"image"
	"image/png"
	"os"
	"testing"
	"time"

	"github.com/dustinahudson/magic-mirror/internal/layout"
	"github.com/dustinahudson/magic-mirror/internal/mirror"
	"github.com/dustinahudson/magic-mirror/internal/provision"
	"github.com/dustinahudson/magic-mirror/internal/render"
	"github.com/dustinahudson/magic-mirror/internal/store"
	"github.com/dustinahudson/magic-mirror/internal/widget"
)

// newTestCompositor returns a compositor with a clock tile and the setup
// overlay installed, mirroring how main() wires them.
func newTestCompositor(bounds image.Rectangle) *mirror.Compositor {
	comp := mirror.NewCompositor(bounds, testLogger())
	comp.SetPlacements([]mirror.Placement{{
		ID:     "clock",
		Type:   "datetime",
		Pos:    layout.Pos{Col: 0, Row: 0, ColSpan: 5, RowSpan: 3},
		Widget: widget.Build("datetime", json.RawMessage(`{"timeSize":96}`)),
	}})
	comp.SetOverlay(&widget.Setup{})
	return comp
}

func drawCtx(data *store.Store, fonts *render.FontSet) widget.Context {
	return widget.Context{
		Now:   time.Date(2026, 7, 30, 9, 41, 0, 0, time.UTC),
		Loc:   time.UTC,
		Fonts: fonts,
		Data:  data.Load(),
		Units: "imperial",
	}
}

// The overlay must be invisible unless provisioning is actually active,
// otherwise it would blank the mirror during normal operation.
func TestOverlayInactiveWhenNotProvisioning(t *testing.T) {
	t.Parallel()

	bounds := image.Rect(0, 0, 640, 360)
	data := store.New()
	fonts := render.NewFontSet()
	comp := newTestCompositor(bounds)

	comp.Draw(drawCtx(data, fonts)) // first frame is a full repaint

	// Connected state: still no overlay.
	data.Success(provision.KeyProvision, provision.State{Mode: provision.ModeConnected})
	comp.Draw(drawCtx(data, fonts))

	// The clock should be on screen, which it would not be if the overlay
	// had painted over everything.
	if !hasContent(comp.Frame(), image.Rect(0, 0, 320, 180)) {
		t.Error("clock area is blank; the overlay painted when it should not have")
	}
}

func TestOverlayTakesOverInPortalMode(t *testing.T) {
	t.Parallel()

	bounds := image.Rect(0, 0, 1920, 1080)
	data := store.New()
	fonts := render.NewFontSet()
	comp := newTestCompositor(bounds)

	comp.Draw(drawCtx(data, fonts))

	data.Success(provision.KeyProvision, provision.State{
		Mode:     provision.ModePortal,
		SSID:     "MagicMirror-Setup",
		URL:      "http://192.168.4.1",
		Networks: 7,
	})

	dirty := comp.Draw(drawCtx(data, fonts))
	if len(dirty) != 1 || dirty[0] != bounds {
		t.Fatalf("entering portal mode should dirty the whole frame, got %v", dirty)
	}

	// Something must be drawn in the middle of the screen.
	if !hasContent(comp.Frame(), image.Rect(400, 200, 1520, 880)) {
		t.Error("portal mode rendered nothing")
	}

	// Save it so the setup screen can be reviewed like any other layout.
	if dir := os.Getenv("MIRROR_GOLDEN_DIR"); dir != "" {
		_ = os.MkdirAll(dir, 0o755)
		f, err := os.Create(dir + "/setup-portal.png")
		if err == nil {
			defer f.Close()
			_ = png.Encode(f, comp.Frame())
		}
	}
}

// Leaving portal mode must repaint, or the setup instructions would be left
// burned onto the screen over a mirror that is now working.
func TestOverlayClearsWhenPortalEnds(t *testing.T) {
	t.Parallel()

	bounds := image.Rect(0, 0, 640, 360)
	data := store.New()
	fonts := render.NewFontSet()
	comp := newTestCompositor(bounds)

	comp.Draw(drawCtx(data, fonts))
	data.Success(provision.KeyProvision, provision.State{
		Mode: provision.ModePortal, SSID: "MagicMirror-Setup", URL: "http://192.168.4.1",
	})
	comp.Draw(drawCtx(data, fonts))

	data.Success(provision.KeyProvision, provision.State{Mode: provision.ModeConnected})
	dirty := comp.Draw(drawCtx(data, fonts))

	if len(dirty) != 1 || dirty[0] != bounds {
		t.Fatalf("leaving portal mode should dirty the whole frame, got %v", dirty)
	}
	if !hasContent(comp.Frame(), image.Rect(0, 0, 320, 180)) {
		t.Error("clock did not come back after portal mode ended")
	}
}

// hasContent reports whether any pixel in r is lit.
func hasContent(img *image.RGBA, r image.Rectangle) bool {
	r = r.Intersect(img.Bounds())
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			c := img.RGBAAt(x, y)
			if c.R > 24 || c.G > 24 || c.B > 24 {
				return true
			}
		}
	}
	return false
}
