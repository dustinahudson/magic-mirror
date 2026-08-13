package display

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/png"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// Preview is a Display that serves the current frame over HTTP.
//
// This is how you watch the mirror run without a Pi. It is not a debug
// afterthought: it is the same Display interface the framebuffer implements,
// fed the same frames, so what the browser shows is exactly what the panel
// would show.
//
// Clients long-poll /api/wait?since=N, which blocks until a newer frame
// exists, then fetch /frame.png. No polling loop, no wasted requests, and the
// page updates the instant a frame is presented.
type Preview struct {
	bounds image.Rectangle
	srv    *http.Server
	ln     net.Listener

	mu      sync.Mutex
	png     []byte    // most recent frame, encoded
	version uint64    // bumped on every Present
	stamp   time.Time // when that frame was presented
	waiters []chan struct{}

	// lastRequest is when a client last asked for anything, which is how
	// Present decides whether encoding is worth doing at all.
	lastRequest time.Time

	// Stats, purely informational, shown in the preview page header.
	presented uint64
	lastDirty int
}

// previewIdleAfter is how long after the last request the preview stops
// encoding frames nobody is watching.
//
// Encoding happens on the render goroutine, so it is time the compositor is
// not drawing. Generous enough that a client polling at any sane rate never
// sees a gap.
const previewIdleAfter = 10 * time.Second

// touch records client activity, so Present knows somebody is watching.
func (p *Preview) touch() {
	p.mu.Lock()
	p.lastRequest = time.Now()
	p.mu.Unlock()
}

// NewPreview starts an HTTP preview server on addr (e.g. ":8080").
func NewPreview(addr string, bounds image.Rectangle) (*Preview, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("preview listen %s: %w", addr, err)
	}

	p := &Preview{bounds: bounds, ln: ln}

	mux := http.NewServeMux()
	mux.HandleFunc("/", p.handleIndex)
	mux.HandleFunc("/frame.png", p.handleFrame)
	mux.HandleFunc("/api/wait", p.handleWait)
	p.srv = &http.Server{Handler: mux}

	go func() { _ = p.srv.Serve(ln) }()
	return p, nil
}

// Addr is the address the preview is listening on, with the port resolved if
// the caller asked for :0.
func (p *Preview) Addr() string { return p.ln.Addr().String() }

func (p *Preview) Bounds() image.Rectangle { return p.bounds }

func (p *Preview) Present(frame *image.RGBA, dirty []image.Rectangle) error {
	// Encoding happens on the caller's goroutine, which is the render loop —
	// so keep it off the critical path when nobody is watching. If no client
	// has asked for a frame recently, skip the encode entirely.
	//
	// This was described here and never implemented: every frame was encoded
	// whether or not anything was going to look at it. A PNG of 1920x1080 is
	// not free, and it was being paid on the goroutine whose deadline is the
	// one that matters.
	p.mu.Lock()
	idle := p.lastRequest.IsZero() || time.Since(p.lastRequest) > previewIdleAfter
	waiting := len(p.waiters) > 0
	if idle && !waiting {
		// Still count the frame; just do not render a picture of it.
		p.presented++
		p.lastDirty = len(dirty)
		p.mu.Unlock()
		return nil
	}
	p.mu.Unlock()

	var buf bytes.Buffer
	if err := png.Encode(&buf, frame); err != nil {
		return fmt.Errorf("preview encode: %w", err)
	}

	p.mu.Lock()
	p.png = buf.Bytes()
	p.version++
	p.stamp = time.Now()
	p.presented++
	p.lastDirty = len(dirty)
	waiters := p.waiters
	p.waiters = nil
	p.mu.Unlock()

	for _, ch := range waiters {
		close(ch)
	}
	return nil
}

func (p *Preview) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return p.srv.Shutdown(ctx)
}

func (p *Preview) handleFrame(w http.ResponseWriter, r *http.Request) {
	p.touch()
	p.mu.Lock()
	data, version := p.png, p.version
	p.mu.Unlock()

	if data == nil {
		http.Error(w, "no frame yet", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Frame-Version", strconv.FormatUint(version, 10))
	_, _ = w.Write(data)
}

// handleWait blocks until a frame newer than ?since= is available, then
// returns its version. Falls back to a timeout so proxies do not kill it.
func (p *Preview) handleWait(w http.ResponseWriter, r *http.Request) {
	p.touch()
	since, _ := strconv.ParseUint(r.URL.Query().Get("since"), 10, 64)

	p.mu.Lock()
	if p.version > since {
		v, n, d := p.version, p.presented, p.lastDirty
		p.mu.Unlock()
		writeWait(w, v, n, d)
		return
	}
	ch := make(chan struct{})
	p.waiters = append(p.waiters, ch)
	p.mu.Unlock()

	select {
	case <-ch:
	case <-r.Context().Done():
		return
	case <-time.After(30 * time.Second):
	}

	p.mu.Lock()
	v, n, d := p.version, p.presented, p.lastDirty
	p.mu.Unlock()
	writeWait(w, v, n, d)
}

func writeWait(w http.ResponseWriter, version, presented uint64, dirty int) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	fmt.Fprintf(w, `{"version":%d,"frames":%d,"dirty":%d}`, version, presented, dirty)
}

func (p *Preview) handleIndex(w http.ResponseWriter, r *http.Request) {
	p.touch()
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(previewHTML))
}

const previewHTML = `<!doctype html>
<meta charset="utf-8">
<title>Magic Mirror — preview</title>
<style>
  :root { color-scheme: dark; }
  * { box-sizing: border-box; }
  body {
    margin: 0; background: #0b0b0d; color: #8b8b96;
    font: 13px/1.5 ui-monospace, SFMono-Regular, Menlo, monospace;
    display: flex; flex-direction: column; height: 100vh;
  }
  header {
    display: flex; gap: 1.5rem; align-items: baseline;
    padding: .6rem 1rem; border-bottom: 1px solid #1e1e24; flex: none;
  }
  header b { color: #e6e6ec; font-weight: 600; letter-spacing: .02em; }
  header span { white-space: nowrap; }
  #dot { color: #3fb950; }
  #dot.stale { color: #d29922; }
  #dot.dead { color: #f85149; }
  main { flex: 1; display: grid; place-items: center; padding: 1rem; min-height: 0; }
  img {
    max-width: 100%; max-height: 100%; object-fit: contain;
    border: 1px solid #1e1e24; background: #000;
    image-rendering: pixelated;
  }
</style>
<header>
  <b>Magic Mirror</b>
  <span id="dot">●</span>
  <span>frames <i id="frames">0</i></span>
  <span>dirty rects <i id="dirty">–</i></span>
  <span>last <i id="ago">–</i></span>
</header>
<main><img id="fb" alt="mirror framebuffer"></main>
<script>
let version = 0, last = Date.now(), failures = 0;
const $ = id => document.getElementById(id);

function paint() {
  $('fb').src = '/frame.png?v=' + version;
  last = Date.now();
}

async function loop() {
  for (;;) {
    try {
      const r = await fetch('/api/wait?since=' + version);
      if (!r.ok) throw new Error(r.status);
      const j = await r.json();
      failures = 0;
      if (j.version > version) { version = j.version; paint(); }
      $('frames').textContent = j.frames;
      $('dirty').textContent = j.dirty === 0 ? 'full' : j.dirty;
    } catch (e) {
      // App restarted or died. Keep trying — init respawns it, and the
      // preview should reconnect on its own rather than needing a reload.
      failures++;
      await new Promise(r => setTimeout(r, 500));
    }
  }
}

setInterval(() => {
  const age = (Date.now() - last) / 1000;
  $('ago').textContent = age < 1 ? 'now' : age.toFixed(0) + 's ago';
  const dot = $('dot');
  dot.className = failures > 2 ? 'dead' : age > 5 ? 'stale' : '';
}, 250);

paint();
loop();
</script>
`
