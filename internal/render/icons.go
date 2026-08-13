package render

import (
	"image"
	"sync"

	"github.com/dustinahudson/magic-mirror/assets"
)

// IconCache holds scaled icons.
//
// CatmullRom scaling a PNG is far too slow to do per frame on an ARMv6 core,
// but icons only change when the forecast does — so scale once per size and
// keep it. The cache is bounded in practice by the handful of sizes the
// layout actually asks for.
type IconCache struct {
	mu     sync.Mutex
	scaled map[iconKey]*image.RGBA
}

type iconKey struct {
	name string
	w, h int
}

// NewIconCache returns an empty cache.
func NewIconCache() *IconCache {
	return &IconCache{scaled: map[iconKey]*image.RGBA{}}
}

// maxCachedIcons bounds the scaled-artwork cache.
//
// Keyed by name and pixel size, and the sizes are computed from tile geometry
// and font metrics rather than chosen from a list — so the number of distinct
// entries is decided by arithmetic, not by anything that counted them. Each
// one holds a scaled RGBA image, and this process runs for months.
//
// Far above what a layout produces: a handful of sizes across the icon set.
// Past it, icons still render, just rescaled each time — the same trade the
// glyph cache makes.
const maxCachedIcons = 512

// Get returns the named icon scaled to fit a w×h box, preserving aspect
// ratio. Reports false when no such icon is embedded.
func (c *IconCache) Get(name string, w, h int) (*image.RGBA, bool) {
	if name == "" || w <= 0 || h <= 0 {
		return nil, false
	}

	key := iconKey{name, w, h}

	c.mu.Lock()
	defer c.mu.Unlock()
	if img, ok := c.scaled[key]; ok {
		return img, img != nil
	}

	src, ok := assets.Icon(name)
	if !ok {
		// Cache the miss too: a config naming a nonexistent icon should not
		// re-hit the asset lookup every frame.
		if len(c.scaled) < maxCachedIcons {
			c.scaled[key] = nil
		}
		return nil, false
	}

	out := FitScale(src, w, h)
	if len(c.scaled) < maxCachedIcons {
		c.scaled[key] = out
	}
	return out, true
}

// defaultIcons is the process-wide cache. Icons are immutable embedded
// assets, so a single shared cache is correct and avoids every widget
// carrying its own copy of the same scaled artwork.
var defaultIcons = NewIconCache()

// Icon returns a scaled icon from the shared cache.
func Icon(name string, w, h int) (*image.RGBA, bool) {
	return defaultIcons.Get(name, w, h)
}
