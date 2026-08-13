package display

import (
	"errors"
	"fmt"
	"image"
)

// fitBounds reconciles the geometry a framebuffer reports with the memory it
// actually mapped, and is deliberately separate from the ioctls so it can be
// tested without a device.
//
// Every blit indexes the mapping at y*stride + x*bpp, computed from the
// reported resolution. Nothing guarantees the two agree: a driver can report a
// virtual resolution larger than the memory behind it, or a line length that
// does not cover the width. Go bounds-checks the slice, so a mismatch is not
// silent corruption — it is a panic inside Present, on the render goroutine,
// where no recover is watching. That is a dead process, a restart loop, and
// somebody driving out to take the card out of a mirror.
//
// So the mapping wins. Where the geometry claims more rows than the memory
// holds, the display renders short and says so, which is a mirror with a band
// of black at the bottom instead of no mirror at all.
func fitBounds(width, height, stride, bpp, memLen int) (image.Rectangle, error) {
	if width <= 0 || height <= 0 {
		return image.Rectangle{}, fmt.Errorf("empty geometry %dx%d", width, height)
	}
	if bpp <= 0 {
		return image.Rectangle{}, fmt.Errorf("%d bytes per pixel", bpp)
	}
	if stride <= 0 {
		return image.Rectangle{}, errors.New("device reports a zero line length")
	}
	if stride < width*bpp {
		return image.Rectangle{}, fmt.Errorf(
			"line length %d cannot hold %d pixels at %d bytes each", stride, width, bpp)
	}

	rows := memLen / stride
	if rows <= 0 {
		return image.Rectangle{}, fmt.Errorf(
			"mapped %d bytes, too small for one %d-byte line", memLen, stride)
	}
	if rows < height {
		height = rows
	}
	return image.Rect(0, 0, width, height), nil
}
