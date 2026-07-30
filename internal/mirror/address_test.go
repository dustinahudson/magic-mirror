package mirror_test

import (
	"encoding/json"
	"image"
	"testing"
	"time"

	"github.com/dustinahudson/magic-mirror/internal/layout"
	"github.com/dustinahudson/magic-mirror/internal/mirror"
	"github.com/dustinahudson/magic-mirror/internal/render"
	"github.com/dustinahudson/magic-mirror/internal/store"
	"github.com/dustinahudson/magic-mirror/internal/widget"
)

// litIn counts pixels differing from the background inside r.
func litIn(img *image.RGBA, r image.Rectangle) int {
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

// corner is the region the address label occupies.
func corner(b image.Rectangle) image.Rectangle {
	return image.Rect(b.Max.X-320, b.Max.Y-40, b.Max.X, b.Max.Y)
}

func addressCompositor(bounds image.Rectangle, addr *string) *mirror.Compositor {
	comp := mirror.NewCompositor(bounds, testLogger())
	comp.SetPlacements([]mirror.Placement{{
		ID:     "clock",
		Type:   "datetime",
		Pos:    layout.Pos{Col: 0, Row: 0, ColSpan: 12, RowSpan: 16},
		Widget: widget.Build("datetime", json.RawMessage(`{"timeSize":96}`)),
	}})
	comp.SetAddress(func() string { return *addr })
	return comp
}

// The mirror has to say where to reach it. Someone who cannot get to the
// settings page cannot add a widget either, so this must not depend on the
// layout.
func TestAddressIsDrawn(t *testing.T) {
	bounds := image.Rect(0, 0, 1920, 1080)
	addr := "http://192.168.1.46"
	comp := addressCompositor(bounds, &addr)

	data := store.New()
	fonts := render.NewFontSet()
	comp.Draw(drawCtx(data, fonts))

	if litIn(comp.Frame(), corner(bounds)) == 0 {
		t.Fatal("no address drawn in the corner")
	}
}

// An empty layout is the case that matters most: it is what a mirror looks
// like after someone deletes the wrong panel, and it is precisely when they
// need to know where to go to undo it.
func TestAddressSurvivesAnEmptyLayout(t *testing.T) {
	bounds := image.Rect(0, 0, 1920, 1080)
	addr := "http://192.168.1.46"

	comp := mirror.NewCompositor(bounds, testLogger())
	comp.SetPlacements(nil)
	comp.SetAddress(func() string { return addr })

	comp.Draw(drawCtx(store.New(), render.NewFontSet()))

	if litIn(comp.Frame(), corner(bounds)) == 0 {
		t.Fatal("a mirror with no widgets does not show its address")
	}
}

// A tile repainting over the corner must not erase the label. The clock
// covers the whole frame here and repaints every second, so without a
// redraw the address would survive exactly one frame.
func TestAddressSurvivesATileRepaint(t *testing.T) {
	bounds := image.Rect(0, 0, 1920, 1080)
	addr := "http://192.168.1.46"
	comp := addressCompositor(bounds, &addr)

	data := store.New()
	fonts := render.NewFontSet()
	ctx := drawCtx(data, fonts)
	comp.Draw(ctx)

	before := litIn(comp.Frame(), corner(bounds))

	// A second later: the clock's key changes, its tile is cleared and
	// redrawn, and that tile covers the corner.
	ctx.Now = ctx.Now.Add(time.Second)
	dirty := comp.Draw(ctx)
	if len(dirty) == 0 {
		t.Fatal("clock did not repaint; the test proves nothing")
	}

	if after := litIn(comp.Frame(), corner(bounds)); after != before {
		t.Errorf("address changed after a tile repainted over it: %d lit, was %d", after, before)
	}
}

// No address is better than a wrong one: before the network is up there is
// nothing anyone could type.
func TestNoAddressBeforeTheNetworkIsUp(t *testing.T) {
	bounds := image.Rect(0, 0, 1920, 1080)
	addr := ""
	comp := addressCompositor(bounds, &addr)

	comp.Draw(drawCtx(store.New(), render.NewFontSet()))

	if litIn(comp.Frame(), corner(bounds)) != 0 {
		t.Error("drew something with no address to show")
	}
}

// Once an address appears it must show up without waiting for a full
// repaint, and the region it occupies has to be reported as changed or the
// display never receives it.
func TestAddressAppearingIsPresented(t *testing.T) {
	bounds := image.Rect(0, 0, 1920, 1080)
	addr := ""
	comp := addressCompositor(bounds, &addr)

	data := store.New()
	fonts := render.NewFontSet()
	ctx := drawCtx(data, fonts)
	comp.Draw(ctx) // full repaint, no address yet

	addr = "http://192.168.1.46"
	ctx.Now = ctx.Now.Add(time.Second)
	dirty := comp.Draw(ctx)

	if litIn(comp.Frame(), corner(bounds)) == 0 {
		t.Fatal("address did not appear")
	}
	covered := false
	for _, d := range dirty {
		if d.Overlaps(corner(bounds)) {
			covered = true
		}
	}
	if !covered {
		t.Error("the address was drawn but its region was never reported as dirty")
	}
}
