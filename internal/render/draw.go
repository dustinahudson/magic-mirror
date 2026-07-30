package render

import (
	"image"
	"image/color"
	"image/draw"
	"math"

	xdraw "golang.org/x/image/draw"
	"golang.org/x/image/vector"
)

// Palette. A mirror is white-on-black by construction — it is a display
// behind half-silvered glass, so anything that is not lit reads as mirror.
// Values carried over from the v1 widgets.
var (
	Background = color.RGBA{0, 0, 0, 255}
	Primary    = color.RGBA{255, 255, 255, 255}
	Secondary  = color.RGBA{180, 180, 180, 255}
	Muted      = color.RGBA{120, 120, 120, 255}
	Faint      = color.RGBA{70, 70, 70, 255}

	// Status colours, used sparingly — a mirror covered in traffic lights
	// stops being a mirror.
	OK    = color.RGBA{82, 250, 127, 255}
	Warn  = color.RGBA{250, 190, 82, 255}
	Error = color.RGBA{250, 82, 88, 255}
)

// Gray returns a neutral grey at the given level.
func Gray(v uint8) color.RGBA { return color.RGBA{v, v, v, 255} }

// Alpha returns c with its alpha replaced, premultiplying the channels so it
// composites correctly with draw.Over.
func Alpha(c color.RGBA, a uint8) color.RGBA {
	f := float64(a) / 255
	return color.RGBA{
		R: uint8(float64(c.R) * f),
		G: uint8(float64(c.G) * f),
		B: uint8(float64(c.B) * f),
		A: a,
	}
}

// ParseHexColor accepts "#RGB", "#RRGGBB" and "#RRGGBBAA". Any parse failure
// yields ok=false rather than a silently wrong colour — a mistyped calendar
// colour should surface in validation, not paint something arbitrary.
func ParseHexColor(s string) (color.RGBA, bool) {
	if len(s) == 0 || s[0] != '#' {
		return color.RGBA{}, false
	}
	h := s[1:]
	hex := func(b byte) (uint8, bool) {
		switch {
		case b >= '0' && b <= '9':
			return b - '0', true
		case b >= 'a' && b <= 'f':
			return b - 'a' + 10, true
		case b >= 'A' && b <= 'F':
			return b - 'A' + 10, true
		}
		return 0, false
	}
	pair := func(i int) (uint8, bool) {
		hi, ok1 := hex(h[i])
		lo, ok2 := hex(h[i+1])
		return hi<<4 | lo, ok1 && ok2
	}

	switch len(h) {
	case 3:
		var out [3]uint8
		for i := range 3 {
			v, ok := hex(h[i])
			if !ok {
				return color.RGBA{}, false
			}
			out[i] = v<<4 | v
		}
		return color.RGBA{out[0], out[1], out[2], 255}, true
	case 6, 8:
		var out [4]uint8
		out[3] = 255
		n := len(h) / 2
		for i := range n {
			v, ok := pair(i * 2)
			if !ok {
				return color.RGBA{}, false
			}
			out[i] = v
		}
		return color.RGBA{out[0], out[1], out[2], out[3]}, true
	}
	return color.RGBA{}, false
}

// Fill paints a solid rectangle.
func Fill(dst *image.RGBA, r image.Rectangle, c color.Color) {
	draw.Draw(dst, r.Intersect(dst.Bounds()), image.NewUniform(c), image.Point{}, draw.Src)
}

// FillOver paints a rectangle with alpha blending.
func FillOver(dst *image.RGBA, r image.Rectangle, c color.Color) {
	draw.Draw(dst, r.Intersect(dst.Bounds()), image.NewUniform(c), image.Point{}, draw.Over)
}

// Clear resets a rectangle to the background colour. Widgets call this before
// redrawing so stale pixels never show through.
func Clear(dst *image.RGBA, r image.Rectangle) { Fill(dst, r, Background) }

// Stroke outlines a rectangle with the given border width, drawn inward.
func Stroke(dst *image.RGBA, r image.Rectangle, w int, c color.Color) {
	if w <= 0 || r.Empty() {
		return
	}
	Fill(dst, image.Rect(r.Min.X, r.Min.Y, r.Max.X, r.Min.Y+w), c)
	Fill(dst, image.Rect(r.Min.X, r.Max.Y-w, r.Max.X, r.Max.Y), c)
	Fill(dst, image.Rect(r.Min.X, r.Min.Y+w, r.Min.X+w, r.Max.Y-w), c)
	Fill(dst, image.Rect(r.Max.X-w, r.Min.Y+w, r.Max.X, r.Max.Y-w), c)
}

// HLine draws a horizontal rule.
func HLine(dst *image.RGBA, x0, x1, y, w int, c color.Color) {
	Fill(dst, image.Rect(x0, y, x1, y+w), c)
}

// VLine draws a vertical rule.
func VLine(dst *image.RGBA, x, y0, y1, w int, c color.Color) {
	Fill(dst, image.Rect(x, y0, x+w, y1), c)
}

// FillRounded paints an anti-aliased rounded rectangle.
//
// Uses a vector rasteriser rather than hand-rolled corner arcs: the corners
// are the only place aliasing is visible on a mirror, and getting them
// smooth for free is worth the allocation.
func FillRounded(dst *image.RGBA, r image.Rectangle, radius int, c color.Color) {
	if r.Empty() {
		return
	}
	maxR := min(r.Dx(), r.Dy()) / 2
	if radius > maxR {
		radius = maxR
	}
	if radius <= 0 {
		Fill(dst, r, c)
		return
	}

	w, h := r.Dx(), r.Dy()
	ra := vector.NewRasterizer(w, h)
	rad := float32(radius)
	fw, fh := float32(w), float32(h)

	// Corner arcs approximated with cubic Béziers; k is the standard
	// circle-to-Bézier constant.
	const k = 0.5522847498
	ck := rad * k

	ra.MoveTo(rad, 0)
	ra.LineTo(fw-rad, 0)
	ra.CubeTo(fw-rad+ck, 0, fw, rad-ck, fw, rad)
	ra.LineTo(fw, fh-rad)
	ra.CubeTo(fw, fh-rad+ck, fw-rad+ck, fh, fw-rad, fh)
	ra.LineTo(rad, fh)
	ra.CubeTo(rad-ck, fh, 0, fh-rad+ck, 0, fh-rad)
	ra.LineTo(0, rad)
	ra.CubeTo(0, rad-ck, rad-ck, 0, rad, 0)
	ra.ClosePath()

	mask := image.NewAlpha(image.Rect(0, 0, w, h))
	ra.Draw(mask, mask.Bounds(), image.Opaque, image.Point{})

	draw.DrawMask(dst, r, image.NewUniform(c), image.Point{}, mask, image.Point{}, draw.Over)
}

// FillCircle paints an anti-aliased filled circle centred at (cx, cy).
func FillCircle(dst *image.RGBA, cx, cy, radius int, c color.Color) {
	r := image.Rect(cx-radius, cy-radius, cx+radius, cy+radius)
	FillRounded(dst, r, radius, c)
}

// DrawImage composites src into dst with its top-left at pt.
func DrawImage(dst *image.RGBA, src image.Image, pt image.Point) {
	b := src.Bounds()
	draw.Draw(dst, image.Rectangle{Min: pt, Max: pt.Add(b.Size())}, src, b.Min, draw.Over)
}

// DrawImageTinted composites src into dst using src's alpha as a mask and
// painting it in a flat colour.
//
// The weather icons are monochrome artwork; tinting lets one PNG serve both
// the bright "now" slot and the dimmer forecast row without shipping two
// copies.
func DrawImageTinted(dst *image.RGBA, src image.Image, pt image.Point, c color.Color) {
	b := src.Bounds()
	mask := image.NewAlpha(image.Rect(0, 0, b.Dx(), b.Dy()))
	for y := 0; y < b.Dy(); y++ {
		for x := 0; x < b.Dx(); x++ {
			_, _, _, a := src.At(b.Min.X+x, b.Min.Y+y).RGBA()
			mask.SetAlpha(x, y, color.Alpha{A: uint8(a >> 8)})
		}
	}
	draw.DrawMask(dst,
		image.Rectangle{Min: pt, Max: pt.Add(b.Size())},
		image.NewUniform(c), image.Point{},
		mask, image.Point{}, draw.Over)
}

// Scale resizes src to the given size with a quality filter.
//
// Callers are expected to cache the result. This is far too slow to run per
// frame on an ARMv6 core, but icons only change when the forecast does.
func Scale(src image.Image, w, h int) *image.RGBA {
	out := image.NewRGBA(image.Rect(0, 0, w, h))
	xdraw.CatmullRom.Scale(out, out.Bounds(), src, src.Bounds(), xdraw.Over, nil)
	return out
}

// FitScale resizes src to fit inside a w×h box while preserving aspect ratio.
func FitScale(src image.Image, w, h int) *image.RGBA {
	b := src.Bounds()
	if b.Dx() == 0 || b.Dy() == 0 {
		return image.NewRGBA(image.Rect(0, 0, 0, 0))
	}
	s := math.Min(float64(w)/float64(b.Dx()), float64(h)/float64(b.Dy()))
	return Scale(src, max(1, int(float64(b.Dx())*s)), max(1, int(float64(b.Dy())*s)))
}

// Center returns the origin that centres a w×h box inside r.
func Center(r image.Rectangle, w, h int) image.Point {
	return image.Pt(r.Min.X+(r.Dx()-w)/2, r.Min.Y+(r.Dy()-h)/2)
}
