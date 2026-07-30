package display

import (
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"sync"
)

// PNGWriter is a Display that writes each frame to a numbered PNG file.
//
// Used for golden-image tests and for capturing a sequence to look at later.
// Not intended for watching live — that is what Preview is for.
type PNGWriter struct {
	dir    string
	bounds image.Rectangle

	mu    sync.Mutex
	n     int
	limit int // 0 = unlimited
}

// NewPNGWriter writes frames into dir, creating it if needed. If limit > 0,
// only the first limit frames are written and later ones are dropped, so an
// unattended run cannot fill a disk.
func NewPNGWriter(dir string, bounds image.Rectangle, limit int) (*PNGWriter, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("png output dir: %w", err)
	}
	return &PNGWriter{dir: dir, bounds: bounds, limit: limit}, nil
}

func (p *PNGWriter) Bounds() image.Rectangle { return p.bounds }

func (p *PNGWriter) Present(frame *image.RGBA, _ []image.Rectangle) error {
	p.mu.Lock()
	if p.limit > 0 && p.n >= p.limit {
		p.mu.Unlock()
		return nil
	}
	n := p.n
	p.n++
	p.mu.Unlock()

	path := filepath.Join(p.dir, fmt.Sprintf("frame-%05d.png", n))
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create %s: %w", path, err)
	}
	defer f.Close()
	if err := png.Encode(f, frame); err != nil {
		return fmt.Errorf("encode %s: %w", path, err)
	}
	return nil
}

func (p *PNGWriter) Close() error { return nil }

// Discard is a Display that accepts frames and drops them. Useful in tests
// that exercise the render loop without caring about pixels.
type Discard struct{ B image.Rectangle }

func (d Discard) Bounds() image.Rectangle                    { return d.B }
func (Discard) Present(*image.RGBA, []image.Rectangle) error { return nil }
func (Discard) Close() error                                 { return nil }
