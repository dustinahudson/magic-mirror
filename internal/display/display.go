// Package display owns the pixel sink.
//
// The renderer holds a Display and nothing else — no HTTP client, no data
// sources, no locks shared with fetchers. That is the invariant the whole
// design rests on: a hung API cannot stall a frame, because the goroutine
// that presents frames has no way to perform I/O against anything but the
// framebuffer.
package display

import (
	"image"
)

// Display is a sink for rendered frames.
//
// Present is given the full frame plus the rectangles that actually changed
// since the previous call. Backends are free to ignore the dirty list and
// blit everything (the preview backend does), but the fbdev backend uses it:
// a full 1920x1080 ARGB blit is ~8.3MB, which costs real milliseconds on an
// ARMv6 core, while a ticking clock usually dirties one small rect.
type Display interface {
	// Bounds is the pixel extent of the display. Constant for a given Display.
	Bounds() image.Rectangle

	// Present pushes frame to the device. dirty may be nil, meaning "all of it".
	Present(frame *image.RGBA, dirty []image.Rectangle) error

	// Close releases the device.
	Close() error
}

// Tee fans one frame out to several displays. Used to drive the real
// framebuffer and a live preview at the same time, so what you see in a
// browser is the same pixels the panel is showing.
type Tee []Display

func (t Tee) Bounds() image.Rectangle {
	if len(t) == 0 {
		return image.Rectangle{}
	}
	return t[0].Bounds()
}

// Present pushes to every backend. It does not stop at the first error: a
// failing preview must never take down the real display. The first error is
// returned once all backends have been attempted.
func (t Tee) Present(frame *image.RGBA, dirty []image.Rectangle) error {
	var firstErr error
	for _, d := range t {
		if err := d.Present(frame, dirty); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (t Tee) Close() error {
	var firstErr error
	for _, d := range t {
		if err := d.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// union returns the smallest rectangle covering every rect in rs, clipped to
// bounds. An empty list yields bounds — "nil dirty means everything".
func union(rs []image.Rectangle, bounds image.Rectangle) image.Rectangle {
	if len(rs) == 0 {
		return bounds
	}
	out := rs[0]
	for _, r := range rs[1:] {
		out = out.Union(r)
	}
	return out.Intersect(bounds)
}
