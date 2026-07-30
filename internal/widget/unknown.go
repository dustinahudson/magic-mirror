package widget

import (
	"image"

	"github.com/dustinahudson/magic-mirror/internal/render"
)

// Unknown stands in for a widget that could not be built.
//
// Two cases produce one: a type name this binary does not know (a config
// written by a newer version, then rolled back), and a widget whose New
// rejected its config. Neither may prevent the mirror from booting or affect
// any other tile — so the failure becomes a labelled placeholder in exactly
// the slot the broken widget would have occupied.
//
// It says what is wrong on screen rather than failing silently, for the same
// reason the mirror never renders placeholder weather: a fault the operator
// cannot see is worse than a visible one.
type Unknown struct {
	Type   string
	Reason string
}

func (u *Unknown) Key(Context) string { return "unknown:" + u.Type + ":" + u.Reason }

func (u *Unknown) Render(dst *image.RGBA, bounds image.Rectangle, ctx Context) {
	render.Stroke(dst, bounds, 1, render.Faint)

	label, err := ctx.Fonts.Face(render.Regular, 18)
	if err != nil {
		return
	}
	small, err := ctx.Fonts.Face(render.Light, 14)
	if err != nil {
		return
	}

	inner := bounds.Inset(12)
	if inner.Empty() {
		return
	}

	y := inner.Min.Y
	label.DrawTop(dst, inner.Min.X, y, label.Truncate(u.Type, inner.Dx()), render.Muted)
	y += label.Height() + 4

	for _, line := range small.Wrap(u.Reason, inner.Dx(), 3) {
		if y+small.Height() > inner.Max.Y {
			break
		}
		small.DrawTop(dst, inner.Min.X, y, line, render.Faint)
		y += small.Height()
	}
}
