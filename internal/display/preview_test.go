package display

import (
	"image"
	"net/http"
	"testing"
	"time"
)

// The preview encodes on the render goroutine, so every frame it encodes is
// time the compositor is not drawing. The file said it skipped that work when
// nobody was watching and did not, so both halves are worth pinning down: it
// must skip when idle, and it must not skip when someone is looking.

func testPreview(t *testing.T) *Preview {
	t.Helper()
	p, err := NewPreview("127.0.0.1:0", image.Rect(0, 0, 320, 240))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Close() })
	return p
}

func frame(bounds image.Rectangle) *image.RGBA { return image.NewRGBA(bounds) }

func TestPreviewSkipsEncodingWhenNobodyIsWatching(t *testing.T) {
	p := testPreview(t)

	for range 5 {
		if err := p.Present(frame(p.Bounds()), nil); err != nil {
			t.Fatal(err)
		}
	}

	p.mu.Lock()
	encoded := p.png != nil
	counted := p.presented
	p.mu.Unlock()

	if encoded {
		t.Error("encoded a frame with no client having ever asked")
	}
	// The frame still happened, and the stats should say so.
	if counted != 5 {
		t.Errorf("presented = %d, want 5 — skipping must not lose the count", counted)
	}
}

func TestPreviewEncodesOnceAClientAsks(t *testing.T) {
	p := testPreview(t)

	// A client loads the page.
	resp, err := http.Get("http://" + p.Addr() + "/")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	if err := p.Present(frame(p.Bounds()), nil); err != nil {
		t.Fatal(err)
	}

	p.mu.Lock()
	encoded := p.png != nil
	p.mu.Unlock()
	if !encoded {
		t.Fatal("no frame encoded after a client asked for the page")
	}

	// And it is actually servable.
	resp, err = http.Get("http://" + p.Addr() + "/frame.png")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("frame.png returned %d", resp.StatusCode)
	}
}

// A client that has gone away stops the work again, rather than leaving the
// render loop encoding forever because someone once opened the page.
func TestPreviewStopsEncodingAfterTheClientLeaves(t *testing.T) {
	p := testPreview(t)

	resp, err := http.Get("http://" + p.Addr() + "/")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	if err := p.Present(frame(p.Bounds()), nil); err != nil {
		t.Fatal(err)
	}

	// Wind the clock back past the idle window.
	p.mu.Lock()
	p.lastRequest = time.Now().Add(-previewIdleAfter - time.Second)
	p.png = nil
	p.mu.Unlock()

	if err := p.Present(frame(p.Bounds()), nil); err != nil {
		t.Fatal(err)
	}

	p.mu.Lock()
	encoded := p.png != nil
	p.mu.Unlock()
	if encoded {
		t.Error("still encoding after the client stopped asking")
	}
}
