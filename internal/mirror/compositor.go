// Package mirror composites widgets into frames.
//
// The compositor is deliberately the only thing that touches the frame
// buffer. It holds no HTTP client, no source handles and no locks shared with
// fetchers — it reads an immutable store snapshot and draws. That is the
// invariant that makes a hung API unable to stall a frame.
package mirror

import (
	"fmt"
	"image"
	"log/slog"

	"github.com/dustinahudson/magic-mirror/internal/layout"
	"github.com/dustinahudson/magic-mirror/internal/render"
	"github.com/dustinahudson/magic-mirror/internal/widget"
)

// Placement is a widget instance positioned on the grid.
type Placement struct {
	ID     string
	Type   string
	Pos    layout.Pos
	Widget widget.Widget
}

// Compositor draws placements into a frame, redrawing only what changed.
//
// A full 1920x1080 repaint is 2 million pixels; on an ARMv6 core that is the
// difference between a clock that ticks and one that stutters. Widgets report
// a key that changes exactly when their output would, and only those tiles
// are cleared and redrawn.
type Compositor struct {
	grid  layout.Grid
	frame *image.RGBA
	log   *slog.Logger

	placements []Placement
	lastKeys   map[string]string
	forceFull  bool

	overlay     widget.Widget
	overlayKey  string
	overlayLive bool
}

// NewCompositor returns a compositor drawing into bounds.
func NewCompositor(bounds image.Rectangle, log *slog.Logger) *Compositor {
	frame := image.NewRGBA(bounds)
	render.Fill(frame, bounds, render.Background)
	return &Compositor{
		grid:      layout.New(bounds),
		frame:     frame,
		log:       log,
		lastKeys:  map[string]string{},
		forceFull: true,
	}
}

// Frame is the current composited image. Only valid to read between Draw
// calls, and only from the render goroutine.
func (c *Compositor) Frame() *image.RGBA { return c.frame }

// Bounds is the frame extent.
func (c *Compositor) Bounds() image.Rectangle { return c.frame.Bounds() }

// SetGrid replaces the grid geometry, forcing a full repaint.
func (c *Compositor) SetGrid(g layout.Grid) {
	g.Bounds = c.frame.Bounds()
	c.grid = g
	c.forceFull = true
}

// SetPlacements swaps in a new widget set.
//
// Called when config changes. The swap is wholesale rather than incremental
// so a config apply can never leave a half-updated screen.
func (c *Compositor) SetPlacements(ps []Placement) {
	c.placements = ps
	c.lastKeys = make(map[string]string, len(ps))
	c.forceFull = true
}

// Placements returns the current widget set.
func (c *Compositor) Placements() []Placement { return c.placements }

// Invalidate forces the next Draw to repaint everything.
func (c *Compositor) Invalidate() { c.forceFull = true }

// SetOverlay installs a widget drawn over the whole frame, on top of
// everything else.
//
// Exists for exactly one situation: the mirror is in a state where the
// normal display is useless and the screen has to say why. Setup mode is
// that state — a mirror showing dashes with no explanation is
// indistinguishable from a broken one.
//
// An overlay whose Key returns "" is inactive and costs nothing: no clear,
// no draw, and the tiles beneath it render normally.
func (c *Compositor) SetOverlay(w widget.Widget) {
	c.overlay = w
	c.forceFull = true
}

// Draw updates the frame and returns the rectangles that changed.
//
// A nil return means nothing changed and there is nothing to present.
func (c *Compositor) Draw(ctx widget.Context) []image.Rectangle {
	// Resolve the overlay first. A transition in either direction changes
	// every pixel, and deciding this after `full` has been latched would
	// defer the repaint by a frame — leaving stale setup instructions on
	// screen for a whole tick after the mirror came back.
	overlayKey := ""
	if c.overlay != nil {
		k, err := safeKey(c.overlay, ctx)
		if err != nil {
			c.log.Error("overlay key panicked", "err", err)
		} else {
			overlayKey = k
		}
	}
	overlayActive := overlayKey != ""

	if overlayActive != c.overlayLive {
		c.forceFull = true
	}

	full := c.forceFull
	c.forceFull = false

	if overlayActive {
		c.overlayLive = true
		if !full && overlayKey == c.overlayKey {
			// Showing and unchanged: nothing to present, and the tiles
			// underneath are deliberately not drawn.
			return nil
		}
		c.overlayKey = overlayKey

		bounds := c.frame.Bounds()
		render.Fill(c.frame, bounds, render.Background)
		if err := safeRender(c.overlay, c.frame, bounds, ctx); err != nil {
			c.log.Error("overlay render panicked", "err", err)
		}
		return []image.Rectangle{bounds}
	}

	c.overlayLive = false
	c.overlayKey = ""

	if full {
		render.Fill(c.frame, c.frame.Bounds(), render.Background)
	}

	var dirty []image.Rectangle
	for i := range c.placements {
		p := &c.placements[i]
		bounds := c.grid.Cell(p.Pos).Intersect(c.frame.Bounds())
		if bounds.Empty() {
			continue
		}

		key, err := safeKey(p.Widget, ctx)
		if err != nil {
			c.log.Error("widget key panicked", "id", p.ID, "type", p.Type, "err", err)
			c.demote(p, "Key panicked: "+err.Error())
			key, _ = safeKey(p.Widget, ctx)
		}

		// On a full repaint every tile is redrawn regardless, because the
		// background beneath it was just wiped.
		if !full && c.lastKeys[p.ID] == key {
			continue
		}

		render.Clear(c.frame, bounds)
		c.renderOne(p, bounds, ctx)
		c.lastKeys[p.ID] = key

		if !full {
			dirty = append(dirty, bounds)
		}
	}

	if full {
		return []image.Rectangle{c.frame.Bounds()}
	}
	return dirty
}

// renderOne draws a single widget, containing any panic to that tile.
//
// A widget that panics becomes a labelled error tile in its own slot and the
// rest of the mirror keeps running. In v1 there was nothing between a bad
// widget and the whole device — this is the same fault-isolation rule applied
// one level down.
func (c *Compositor) renderOne(p *Placement, bounds image.Rectangle, ctx widget.Context) {
	if err := safeRender(p.Widget, c.frame, bounds, ctx); err != nil {
		c.log.Error("widget render panicked",
			"id", p.ID, "type", p.Type, "err", err)
		c.demote(p, "Render panicked: "+err.Error())
		render.Clear(c.frame, bounds)
		// The replacement is an Unknown, which is plain drawing code; if it
		// somehow panics too there is nothing useful left to do but leave
		// the tile blank.
		_ = safeRender(p.Widget, c.frame, bounds, ctx)
	}
}

// demote replaces a misbehaving widget with a placeholder describing the
// fault, so it fails once and shows the reason rather than panicking every
// frame forever.
func (c *Compositor) demote(p *Placement, reason string) {
	if _, already := p.Widget.(*widget.Unknown); already {
		return
	}
	p.Widget = &widget.Unknown{Type: p.Type, Reason: reason}
	delete(c.lastKeys, p.ID)
}

func safeKey(w widget.Widget, ctx widget.Context) (key string, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("%v", r)
		}
	}()
	return w.Key(ctx), nil
}

func safeRender(w widget.Widget, dst *image.RGBA, bounds image.Rectangle, ctx widget.Context) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("%v", r)
		}
	}()
	w.Render(dst, bounds, ctx)
	return nil
}
