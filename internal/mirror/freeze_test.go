package mirror_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/dustinahudson/magic-mirror/internal/layout"
	"github.com/dustinahudson/magic-mirror/internal/mirror"
	"github.com/dustinahudson/magic-mirror/internal/render"
	"github.com/dustinahudson/magic-mirror/internal/source"
	"github.com/dustinahudson/magic-mirror/internal/store"
	"github.com/dustinahudson/magic-mirror/internal/widget"

	"image"
)

// tarpit is an HTTP server that accepts a connection, reads the request, and
// then never responds. It is the precise shape of the failure that used to
// take the mirror down: not a refused connection, not an error, just silence
// on an established connection.
func tarpit(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	t.Cleanup(srv.Close)
	return srv
}

// blackhole listens but never completes a TLS/HTTP exchange at all — the
// connection opens and then nothing happens.
func blackhole(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			// Hold the connection open, read nothing, write nothing.
			go func() { _, _ = io.Copy(io.Discard, conn) }()
		}
	}()
	t.Cleanup(func() { ln.Close() })
	return "http://" + ln.Addr().String()
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// TestRenderLoopSurvivesHungSources is the regression test for the bug this
// rewrite exists to fix.
//
// In v1, src/core/kernel.cpp called FetchCalendar (line 489) and the LVGL
// render tick (line 577) from one loop body, so a calendar server that
// accepted a connection and went quiet stopped the clock — and, once the
// stall passed the 15s watchdog armed at line 600, rebooted the device into
// another hung fetch.
//
// Here the fetchers are pointed at servers that never answer, and the
// assertion is that the render loop keeps producing distinct frames at full
// rate throughout.
func TestRenderLoopSurvivesHungSources(t *testing.T) {
	t.Parallel()

	hung := tarpit(t)
	void := blackhole(t)

	data := store.New()
	mgr := source.NewManager(data, testLogger())
	mgr.Add(&stallingSource{key: "weather", url: hung.URL, interval: 50 * time.Millisecond})
	mgr.Add(&stallingSource{key: "calendar", url: void, interval: 50 * time.Millisecond})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	fetchDone := make(chan struct{})
	go func() {
		defer close(fetchDone)
		mgr.Run(ctx)
	}()

	// A clock widget whose key changes every tick, so a stalled render loop
	// shows up as missing frames rather than as identical ones.
	comp := mirror.NewCompositor(image.Rect(0, 0, 320, 240), testLogger())
	comp.SetPlacements([]mirror.Placement{{
		ID:     "clock",
		Type:   "datetime",
		Pos:    layout.Pos{Col: 0, Row: 0, ColSpan: 12, RowSpan: 4},
		Widget: widget.Build("datetime", json.RawMessage(`{"timeSize":32}`)),
	}})

	fonts := render.NewFontSet()

	const (
		ticks    = 40
		tickRate = 10 * time.Millisecond
	)

	var (
		painted int
		now     = time.Date(2025, 6, 10, 9, 0, 0, 0, time.UTC)
		slowest time.Duration
	)

	for i := range ticks {
		// Advance a second per tick so the clock's seconds field moves and
		// every tick is genuinely a new frame.
		frameStart := time.Now()
		dirty := comp.Draw(widget.Context{
			Now:   now.Add(time.Duration(i) * time.Second),
			Loc:   time.UTC,
			Fonts: fonts,
			Data:  data.Load(),
			Units: "imperial",
		})
		elapsed := time.Since(frameStart)
		if elapsed > slowest {
			slowest = elapsed
		}
		if len(dirty) > 0 {
			painted++
		}
		time.Sleep(tickRate)
	}

	if painted != ticks {
		t.Errorf("painted %d of %d frames; a hung source stalled the render loop",
			painted, ticks)
	}

	// The real assertion. Every fetch is stuck against a server that will
	// never answer, so if the render path touched them at all, some frame
	// would have taken multiple seconds.
	if slowest > 500*time.Millisecond {
		t.Errorf("slowest frame took %v; render loop is coupled to network I/O", slowest)
	}

	// And the sources should have recorded their failures rather than
	// silently presenting invented data.
	snap := data.Load()
	for _, key := range []string{"weather", "calendar"} {
		e, ok := snap.Entry(key)
		if !ok {
			continue // may not have timed out yet, which is fine
		}
		if e.Status == store.Fresh {
			t.Errorf("source %q reports Fresh despite never receiving a response", key)
		}
	}

	cancel()
	select {
	case <-fetchDone:
	case <-time.After(5 * time.Second):
		t.Error("fetch manager did not stop on context cancellation")
	}
}

// TestFailedSourceRendersNoFakeData guards the other half of the rule: a
// source that has never succeeded must not produce a value.
//
// v1's weather_widget.cpp:38-48 seeded 72°F / "Partly Cloudy" / Dallas as
// constructor defaults, so a failed fetch rendered as confident invented
// weather and the operator could not tell working from broken.
func TestFailedSourceRendersNoFakeData(t *testing.T) {
	t.Parallel()

	data := store.New()
	data.Failure("weather", io.ErrUnexpectedEOF)

	r := store.Get[source.Conditions](data.Load(), "weather")
	if _, ok := r.Get(); ok {
		t.Fatal("a never-successful source returned a value")
	}
	if r.Status != store.Failed {
		t.Errorf("Status = %v, want Failed", r.Status)
	}
}

// TestStaleDataSurvivesFailure checks that a failure keeps the last good
// value rather than blanking the display.
func TestStaleDataSurvivesFailure(t *testing.T) {
	t.Parallel()

	data := store.New()
	data.Success("weather", source.Conditions{Temperature: 61.5, City: "Kansas City"})
	data.Failure("weather", io.ErrUnexpectedEOF)

	r := store.Get[source.Conditions](data.Load(), "weather")
	v, ok := r.Get()
	if !ok {
		t.Fatal("previously good data was discarded on failure")
	}
	if v.Temperature != 61.5 {
		t.Errorf("Temperature = %v, want 61.5", v.Temperature)
	}
	if r.Status != store.Stale {
		t.Errorf("Status = %v, want Stale", r.Status)
	}
	if r.LastErr == nil {
		t.Error("LastErr not recorded")
	}
}

// stallingSource issues a real HTTP GET against a server that never answers.
type stallingSource struct {
	key      string
	url      string
	interval time.Duration
}

func (s *stallingSource) Key() string             { return s.key }
func (s *stallingSource) Interval() time.Duration { return s.interval }
func (s *stallingSource) Timeout() time.Duration  { return 200 * time.Millisecond }

func (s *stallingSource) Fetch(ctx context.Context) (any, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := source.HTTPClient(200 * time.Millisecond).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return source.Conditions{}, nil
}
