package render

import "testing"

// The cache is keyed by icon name and pixel size, and those sizes are
// computed from tile geometry and font metrics rather than chosen from a
// list. Nothing counted how many distinct values that arithmetic can produce,
// and each entry holds a scaled image in a process that runs for months.

func TestIconCacheIsBounded(t *testing.T) {
	c := NewIconCache()

	// Comfortably more distinct sizes than any layout produces, without
	// making the suite pay to scale a thousand images.
	for i := 1; i <= maxCachedIcons+64; i++ {
		c.Get("clear_day", i, i)
	}

	c.mu.Lock()
	n := len(c.scaled)
	c.mu.Unlock()

	if n > maxCachedIcons {
		t.Errorf("cache holds %d entries, want at most %d", n, maxCachedIcons)
	}
}

// Past the limit icons must still render — just rescaled each time, which is
// slower rather than wrong.
func TestIconsStillRenderPastTheLimit(t *testing.T) {
	c := NewIconCache()
	for i := 1; i <= maxCachedIcons+8; i++ {
		c.Get("clear_day", i, i)
	}

	img, ok := c.Get("clear_day", 96, 96)
	if !ok {
		t.Fatal("an icon stopped rendering once the cache was full")
	}
	if img == nil || img.Bounds().Empty() {
		t.Error("got an empty image past the cache limit")
	}
}

// The common case has to stay a cache hit, or bounding it would have cost
// the thing it exists for.
func TestRepeatedSizesAreServedFromTheCache(t *testing.T) {
	c := NewIconCache()

	first, ok := c.Get("clear_day", 48, 48)
	if !ok {
		t.Skip("icon set does not contain the sample")
	}
	second, _ := c.Get("clear_day", 48, 48)

	if first != second {
		t.Error("the same icon at the same size was rescaled rather than reused")
	}
}

// A name that does not exist must not send the asset lookup round again on
// every frame.
func TestMissingIconIsRemembered(t *testing.T) {
	c := NewIconCache()

	if _, ok := c.Get("no-such-icon-anywhere", 32, 32); ok {
		t.Fatal("reported success for an icon that does not exist")
	}

	c.mu.Lock()
	_, cached := c.scaled[iconKey{"no-such-icon-anywhere", 32, 32}]
	c.mu.Unlock()
	if !cached {
		t.Error("the miss was not remembered")
	}
}
