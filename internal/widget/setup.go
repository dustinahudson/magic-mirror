package widget

import (
	"fmt"
	"image"

	"github.com/dustinahudson/magic-mirror/internal/provision"
	"github.com/dustinahudson/magic-mirror/internal/render"
	"github.com/dustinahudson/magic-mirror/internal/store"
)

// Setup is the full-screen overlay shown while the mirror is in
// provisioning mode.
//
// Not registered as a placeable widget: it is installed as the compositor's
// overlay and takes the whole frame, because in setup mode every other tile
// has nothing useful to say. A mirror showing dashes with no explanation is
// indistinguishable from a broken one — this is the screen that tells you
// which it is and what to do.
type Setup struct{}

// Key returns "" when provisioning is not active, which is how the
// compositor knows the overlay is inactive and the normal display should
// show through.
func (s *Setup) Key(ctx Context) string {
	st, ok := s.state(ctx)
	if !ok || st.Mode != provision.ModePortal {
		return ""
	}
	return fmt.Sprintf("setup|%s|%s|%d", st.SSID, st.URL, st.Networks)
}

func (s *Setup) state(ctx Context) (provision.State, bool) {
	return store.Get[provision.State](ctx.Data, provision.KeyProvision).Get()
}

func (s *Setup) Render(dst *image.RGBA, bounds image.Rectangle, ctx Context) {
	st, ok := s.state(ctx)
	if !ok {
		return
	}

	// Sized off the frame rather than fixed, so this stays legible whether
	// the panel is 1080p or something smaller.
	h := bounds.Dy()
	titleSize := clampInt(h/14, 24, 96)
	stepSize := clampInt(h/28, 16, 46)
	noteSize := clampInt(h/40, 12, 30)

	title, err := ctx.Fonts.Face(render.Light, titleSize)
	if err != nil {
		return
	}
	step, err := ctx.Fonts.Face(render.Regular, stepSize)
	if err != nil {
		return
	}
	strong, err := ctx.Fonts.Face(render.SemiBold, stepSize)
	if err != nil {
		return
	}
	note, err := ctx.Fonts.Face(render.Light, noteSize)
	if err != nil {
		return
	}

	cx := bounds.Min.X + bounds.Dx()/2
	y := bounds.Min.Y + h/5

	center := func(f *render.Face, s string, c render.RGBA) {
		w := f.Measure(s)
		f.DrawTop(dst, cx-w/2, y, s, c)
		y += f.Height()
	}

	center(title, "Wi-Fi setup", render.Primary)
	y += titleSize / 2

	center(step, "1.  Connect a phone to this network", render.Secondary)
	y += stepSize / 4
	center(strong, st.SSID, render.Primary)
	y += stepSize

	center(step, "2.  A setup page should open by itself", render.Secondary)
	y += stepSize / 4
	center(note, "if it does not, browse to "+st.URL, render.Muted)
	y += stepSize

	// The scan count is worth showing: "0 networks found" and "9 networks
	// found" are completely different problems, and without this the
	// difference is invisible from the sofa.
	switch {
	case st.Networks == 0:
		center(note, "No networks found — the mirror may be out of range", render.Warn)
	case st.Networks == 1:
		center(note, "1 network found nearby", render.Muted)
	default:
		center(note, fmt.Sprintf("%d networks found nearby", st.Networks), render.Muted)
	}

	// The setup network is deliberately open, and saying so is better than
	// leaving someone hunting for a password that does not exist.
	y = bounds.Max.Y - h/6
	msg := "This setup network is open and disappears once the mirror connects."
	nw := note.Measure(msg)
	note.DrawTop(dst, cx-nw/2, y, msg, render.Faint)
}
