package render

import (
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"sync"

	"golang.org/x/image/font"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/font/sfnt"
	"golang.org/x/image/math/fixed"

	"github.com/dustinahudson/magic-mirror/assets"
)

// Weight selects one of the embedded Inter cuts.
type Weight int

const (
	Light Weight = iota
	Regular
	SemiBold
)

func (w Weight) asset() string {
	switch w {
	case Light:
		return assets.FontLight
	case SemiBold:
		return assets.FontSemiBold
	default:
		return assets.FontRegular
	}
}

// FontSet owns the parsed fonts and hands out sized Faces.
//
// Faces are cached by (weight, size) because constructing one parses tables
// and allocates a rasterizer — not something to do per frame.
type FontSet struct {
	mu     sync.Mutex
	parsed map[Weight]*sfnt.Font
	faces  map[faceKey]*Face
}

type faceKey struct {
	w    Weight
	size int // whole pixels; the UI has no need for fractional sizes
}

func NewFontSet() *FontSet {
	return &FontSet{
		parsed: make(map[Weight]*sfnt.Font),
		faces:  make(map[faceKey]*Face),
	}
}

// Face returns a face for the given weight at sizePx pixels em-height.
func (fs *FontSet) Face(w Weight, sizePx int) (*Face, error) {
	fs.mu.Lock()
	defer fs.mu.Unlock()

	key := faceKey{w, sizePx}
	if f, ok := fs.faces[key]; ok {
		return f, nil
	}

	parsed, ok := fs.parsed[w]
	if !ok {
		raw, err := assets.Font(w.asset())
		if err != nil {
			return nil, err
		}
		parsed, err = sfnt.Parse(raw)
		if err != nil {
			return nil, fmt.Errorf("parse font %s: %w", w.asset(), err)
		}
		fs.parsed[w] = parsed
	}

	// DPI 72 makes Size map 1:1 to pixels, which keeps layout arithmetic
	// honest — a 48px font occupies 48px of em box.
	ff, err := opentype.NewFace(parsed, &opentype.FaceOptions{
		Size:    float64(sizePx),
		DPI:     72,
		Hinting: font.HintingFull,
	})
	if err != nil {
		return nil, fmt.Errorf("new face %s@%d: %w", w.asset(), sizePx, err)
	}

	f := &Face{
		face:    ff,
		metrics: ff.Metrics(),
		glyphs:  make(map[rune]*glyph, 96),
		size:    sizePx,
	}
	fs.faces[key] = f
	return f, nil
}

// FitFace returns the largest face at or below maxSize whose rendering of
// sample fits inside maxW by maxH.
//
// Widget tiles are sized by the user in grid cells, and a configured font
// size is a preference rather than a promise — a clock at 96px in a tile
// two rows tall has to give. Every size the search visits is cached in the
// FontSet, so repeated calls at a stable tile size cost one map lookup.
//
// Pass maxW or maxH as 0 to leave that dimension unconstrained.
func (fs *FontSet) FitFace(w Weight, maxSize int, sample string, maxW, maxH int) (*Face, error) {
	const minSize = 8
	if maxSize < minSize {
		maxSize = minSize
	}

	fits := func(size int) (bool, error) {
		f, err := fs.Face(w, size)
		if err != nil {
			return false, err
		}
		if maxH > 0 && f.Height() > maxH {
			return false, nil
		}
		if maxW > 0 && sample != "" && f.Measure(sample) > maxW {
			return false, nil
		}
		return true, nil
	}

	// The preferred size usually fits; check it before searching.
	if ok, err := fits(maxSize); err != nil {
		return nil, err
	} else if ok {
		return fs.Face(w, maxSize)
	}

	lo, hi := minSize, maxSize
	for lo < hi {
		mid := (lo + hi + 1) / 2
		ok, err := fits(mid)
		if err != nil {
			return nil, err
		}
		if ok {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	return fs.Face(w, lo)
}

// MustFace is Face with the error promoted to a panic. Only for sizes fixed
// at compile time, where a failure is a build problem rather than a runtime
// one.
func (fs *FontSet) MustFace(w Weight, sizePx int) *Face {
	f, err := fs.Face(w, sizePx)
	if err != nil {
		panic(err)
	}
	return f
}

// glyph is a rasterised rune, cached.
//
// The mask must be a copy: opentype.Face reuses one internal rasterizer
// buffer across Glyph calls, so holding its return value would give every
// cached glyph the shape of the most recent one.
type glyph struct {
	mask    *image.Alpha
	bounds  image.Rectangle // relative to the pen position
	advance int
}

// Face is a sized font with a glyph cache.
//
// Not safe for concurrent use. Only the render goroutine touches a Face,
// and keeping it lock-free matters: on an ARMv6 core, per-glyph mutex
// traffic is not free.
type Face struct {
	face    font.Face
	metrics font.Metrics
	glyphs  map[rune]*glyph
	size    int
}

// Height is the recommended line height in pixels.
func (f *Face) Height() int { return f.metrics.Height.Round() }

// Ascent is the distance from baseline to the top of the em box.
func (f *Face) Ascent() int { return f.metrics.Ascent.Round() }

// Descent is the distance from baseline to the bottom of the em box.
func (f *Face) Descent() int { return f.metrics.Descent.Round() }

// Size is the em size this face was built at.
func (f *Face) Size() int { return f.size }

// Measure returns the advance width of s in pixels.
func (f *Face) Measure(s string) int {
	return font.MeasureString(f.face, s).Round()
}

func (f *Face) glyphFor(r rune) *glyph {
	if g, ok := f.glyphs[r]; ok {
		return g
	}

	dr, mask, maskp, adv, ok := f.face.Glyph(fixed.Point26_6{}, r)
	if !ok {
		// Unmapped rune: cache a blank of the right advance so we neither
		// re-attempt the lookup every frame nor collapse the layout.
		g := &glyph{advance: f.face.Kern(r, r).Round()}
		if a, ok := f.face.GlyphAdvance(r); ok {
			g.advance = a.Round()
		}
		f.glyphs[r] = g
		return g
	}

	m := image.NewAlpha(image.Rect(0, 0, dr.Dx(), dr.Dy()))
	draw.Draw(m, m.Bounds(), mask, maskp, draw.Src)

	g := &glyph{mask: m, bounds: dr, advance: adv.Round()}
	f.glyphs[r] = g
	return g
}

// Draw renders s with its baseline at y and its left edge at x, returning
// the pen position after the last glyph.
//
// Uniform source over an alpha mask into an RGBA destination is the case
// image/draw has a dedicated fast path for, which is why glyphs are stored
// as *image.Alpha rather than pre-coloured.
func (f *Face) Draw(dst *image.RGBA, x, y int, s string, c color.Color) int {
	src := image.NewUniform(c)
	pen := x
	prev := rune(-1)

	for _, r := range s {
		if prev >= 0 {
			pen += f.face.Kern(prev, r).Round()
		}
		g := f.glyphFor(r)
		if g.mask != nil {
			draw.DrawMask(dst,
				g.bounds.Add(image.Pt(pen, y)),
				src, image.Point{},
				g.mask, image.Point{},
				draw.Over)
		}
		pen += g.advance
		prev = r
	}
	return pen
}

// DrawTop renders s with its em-box top at y rather than its baseline.
// Most layout code thinks in terms of boxes, not baselines.
func (f *Face) DrawTop(dst *image.RGBA, x, y int, s string, c color.Color) int {
	return f.Draw(dst, x, y+f.Ascent(), s, c)
}

// Truncate shortens s until it fits maxWidth, appending an ellipsis if
// anything was removed. Returns s unchanged when it already fits.
func (f *Face) Truncate(s string, maxWidth int) string {
	if f.Measure(s) <= maxWidth {
		return s
	}
	const ellipsis = "…"
	ew := f.Measure(ellipsis)
	if ew > maxWidth {
		return ""
	}

	runes := []rune(s)
	// Linear from the end is fine: strings here are short (event titles,
	// city names) and this runs only when text actually overflows.
	for i := len(runes) - 1; i > 0; i-- {
		if f.Measure(string(runes[:i]))+ew <= maxWidth {
			return string(runes[:i]) + ellipsis
		}
	}
	return ellipsis
}

// Wrap breaks s into lines that each fit maxWidth, splitting on spaces.
// A single word longer than maxWidth is truncated rather than overflowing.
func (f *Face) Wrap(s string, maxWidth int, maxLines int) []string {
	if s == "" {
		return nil
	}
	var lines []string
	var cur string

	flush := func() {
		if cur != "" {
			lines = append(lines, cur)
			cur = ""
		}
	}

	for _, word := range splitSpaces(s) {
		cand := word
		if cur != "" {
			cand = cur + " " + word
		}
		if f.Measure(cand) <= maxWidth {
			cur = cand
			continue
		}
		flush()
		if f.Measure(word) > maxWidth {
			cur = f.Truncate(word, maxWidth)
			flush()
			continue
		}
		cur = word
		if maxLines > 0 && len(lines) >= maxLines {
			break
		}
	}
	flush()

	if maxLines > 0 && len(lines) > maxLines {
		lines = lines[:maxLines]
		lines[maxLines-1] = f.Truncate(lines[maxLines-1]+"…", maxWidth)
	}
	return lines
}

func splitSpaces(s string) []string {
	var out []string
	start := -1
	for i, r := range s {
		if r == ' ' || r == '\t' || r == '\n' {
			if start >= 0 {
				out = append(out, s[start:i])
				start = -1
			}
			continue
		}
		if start < 0 {
			start = i
		}
	}
	if start >= 0 {
		out = append(out, s[start:])
	}
	return out
}
