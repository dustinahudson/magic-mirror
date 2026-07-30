// Package assets holds everything baked into the binary: fonts, weather
// icons, and the logo.
//
// v1 shipped its icons as ~17k lines of generated C pixel arrays across
// include/ui/weather_icons.h and src/ui/icons/weather_icons.cpp. Here they
// are just PNG files, embedded at build time and decoded at startup, so
// updating an icon means replacing a PNG.
package assets

import (
	"embed"
	"fmt"
	"image"
	_ "image/png"
	"io/fs"
	"strings"
	"sync"
)

//go:embed fonts/Inter-Light.ttf fonts/Inter-Regular.ttf fonts/Inter-SemiBold.ttf
var fontFS embed.FS

//go:embed icons/*.png
var iconFS embed.FS

// Font weights available to the renderer.
const (
	FontLight    = "fonts/Inter-Light.ttf"
	FontRegular  = "fonts/Inter-Regular.ttf"
	FontSemiBold = "fonts/Inter-SemiBold.ttf"
)

// Font returns the raw TTF bytes for one of the Font* constants.
func Font(name string) ([]byte, error) {
	b, err := fontFS.ReadFile(name)
	if err != nil {
		return nil, fmt.Errorf("embedded font %q: %w", name, err)
	}
	return b, nil
}

var (
	iconOnce sync.Once
	iconMap  map[string]image.Image
	iconErr  error
)

// Icons returns every embedded icon keyed by its base name without
// extension, e.g. "clear_day", "moon_waxing_gibbous".
//
// Decoding happens once, lazily, on first use — it is a few hundred
// kilobytes of PNG and there is no reason to pay for it before the first
// frame that needs an icon.
func Icons() (map[string]image.Image, error) {
	iconOnce.Do(func() {
		iconMap = make(map[string]image.Image)
		entries, err := fs.ReadDir(iconFS, "icons")
		if err != nil {
			iconErr = fmt.Errorf("read embedded icons: %w", err)
			return
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".png") {
				continue
			}
			f, err := iconFS.Open("icons/" + e.Name())
			if err != nil {
				iconErr = fmt.Errorf("open icon %s: %w", e.Name(), err)
				return
			}
			img, _, err := image.Decode(f)
			f.Close()
			if err != nil {
				iconErr = fmt.Errorf("decode icon %s: %w", e.Name(), err)
				return
			}
			iconMap[strings.TrimSuffix(e.Name(), ".png")] = img
		}
	})
	return iconMap, iconErr
}

// Icon returns a single icon by name, reporting whether it exists.
func Icon(name string) (image.Image, bool) {
	m, err := Icons()
	if err != nil {
		return nil, false
	}
	img, ok := m[name]
	return img, ok
}
