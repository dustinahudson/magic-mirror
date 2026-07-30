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

// Address label geometry. Small enough to read only when looked for, inset
// far enough from the edge to survive a panel that overscans.
const (
	addressTextSize = 15
	addressPad      = 10
)

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

	// address is the web address to print in the corner, resolved per frame.
	address     func() string
	addressKey  string
	addressRect image.Rectangle
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

// SetAddress installs a function returning the address to print in the
// corner of every frame, or "" to print nothing.
//
// This is a property of the compositor rather than a widget, and that is the
// whole point. The address is how someone reaches the settings page, and the
// settings page is the only way to add a widget — so a mirror whose address
// depends on a widget being present can be configured into a state where
// nobody can configure it. It has to survive an empty layout.
func (c *Compositor) SetAddress(fn func() string) {
	c.address = fn
	c.forceFull = true
}

// drawAddress prints the address in the bottom-right corner and returns the
// rectangle it occupies, or the empty rectangle when there is nothing to
// print.
//
// Deliberately faint and small: this is a label on the hardware, not
// something to read from across the room. It is there for the moment someone
// needs it and would otherwise have to find the router's client list.
func (c *Compositor) drawAddress(ctx widget.Context) image.Rectangle {
	if c.address == nil {
		return image.Rectangle{}
	}
	addr := c.address()
	if addr == "" {
		return image.Rectangle{}
	}
	face, err := ctx.Fonts.Face(render.Regular, addressTextSize)
	if err != nil {
		return image.Rectangle{}
	}

	b := c.frame.Bounds()
	w, h := face.Measure(addr), face.Height()
	r := image.Rect(b.Max.X-addressPad-w, b.Max.Y-addressPad-h, b.Max.X-addressPad, b.Max.Y-addressPad)
	if r.Min.X < b.Min.X || r.Min.Y < b.Min.Y {
		return image.Rectangle{}
	}

	render.Fill(c.frame, r, render.Background)
	face.DrawTop(c.frame, r.Min.X, r.Min.Y, addr, render.Faint)
	return r
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
		c.drawSeparators()
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

	// The address is drawn after the tiles, so it sits on top of whatever
	// occupies that corner rather than being erased by it.
	if c.address != nil {
		addr := c.address()
		overlapped := false
		for _, d := range dirty {
			if d.Overlaps(c.addressRect) {
				overlapped = true
				break
			}
		}
		// Redraw when the text changed, when a tile underneath repainted
		// over it, or on a full repaint. Otherwise it stays put and costs
		// nothing — the point of dirty-rect tracking is that a static
		// corner label does not force a present every second.
		if full || addr != c.addressKey || overlapped {
			if r := c.drawAddress(ctx); !r.Empty() {
				if !full && (!r.Eq(c.addressRect) || !overlapped) {
					dirty = append(dirty, r)
				}
				c.addressRect = r
			} else {
				c.addressRect = image.Rectangle{}
			}
			c.addressKey = addr
		}
	}

	if full {
		return []image.Rectangle{c.frame.Bounds()}
	}
	return dirty
}

// drawSeparators rules a line above every widget that has another widget
// above it in the same column.
//
// This belongs to the compositor rather than to any widget: only the
// compositor knows what sits above what, and a widget drawing its own top
// rule cannot tell whether it is the first in a column or the third.
//
// The rules live in the grid gap, outside every tile's bounds, so a tile
// redraw never erases one. That also means they are drawn only on a full
// repaint — which is exactly when they can change, since the layout is
// fixed between config applies.
func (c *Compositor) drawSeparators() {
	for i := range c.placements {
		a := c.placements[i].Pos
		if !c.hasWidgetAbove(i, a) {
			continue
		}

		r := c.grid.Cell(a).Intersect(c.frame.Bounds())
		if r.Empty() {
			continue
		}
		// Centre the rule in the gap above the tile.
		y := r.Min.Y - c.grid.GapY/2
		if y <= c.frame.Bounds().Min.Y {
			continue
		}
		render.HLine(c.frame, r.Min.X, r.Max.X, y, 1, render.Faint)
	}
}

// hasWidgetAbove reports whether any other widget overlaps this one
// horizontally and ends above it.
func (c *Compositor) hasWidgetAbove(skip int, a layout.Pos) bool {
	aCols := max(1, a.ColSpan)
	for j := range c.placements {
		if j == skip {
			continue
		}
		b := c.placements[j].Pos
		bCols := max(1, b.ColSpan)

		// Share any column, and finish at or before this one starts.
		if a.Col < b.Col+bCols && b.Col < a.Col+aCols &&
			b.Row+max(1, b.RowSpan) <= a.Row {
			return true
		}
	}
	return false
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
