package web

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dustinahudson/magic-mirror/internal/config"
	"github.com/dustinahudson/magic-mirror/internal/store"
)

// testServer returns a Server wired up without a listener, so handlers can be
// called directly.
func testServer(t *testing.T, configPath string) *Server {
	t.Helper()
	cfg := config.Default()
	cfg.Hostname = "hallway"
	cfg.Weather.Zipcode = "90210"
	cfg.Widgets = nil

	return &Server{
		applier:    NewApplier(cfg),
		data:       store.New(),
		log:        slog.New(slog.NewTextHandler(io.Discard, nil)),
		configPath: configPath,
	}
}

func post(path string, contentType string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, path, strings.NewReader("{}"))
	if contentType != "" {
		r.Header.Set("Content-Type", contentType)
	}
	return r
}

func TestResetRestoresTheShippedConfiguration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	s := testServer(t, path)

	w := httptest.NewRecorder()
	s.handleReset(w, post("/api/reset", "application/json"))

	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}

	// The reply carries the new configuration. /api/config still reports the
	// old one until the render loop takes the staged config, so a page that
	// re-fetched instead would redraw itself with the settings it just reset —
	// and hand them back on the next save.
	var reply struct {
		OK     bool          `json:"ok"`
		Config config.Config `json:"config"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &reply); err != nil {
		t.Fatalf("decode reply: %v (%s)", err, w.Body)
	}
	if reply.Config.Hostname != config.Default().Hostname {
		t.Errorf("reply carried hostname %q, want the default", reply.Config.Hostname)
	}
	if s.applier.Current().Hostname != "hallway" {
		t.Error("Current() changed before the render loop took the staged config")
	}

	// Staged for the render loop, so the mirror changes without a restart.
	staged, ok := s.applier.Take()
	if !ok {
		t.Fatal("reset staged nothing for the render loop")
	}
	want := config.Default()
	if staged.Hostname != want.Hostname {
		t.Errorf("hostname is %q, want the default %q", staged.Hostname, want.Hostname)
	}
	if staged.Weather.Zipcode != want.Weather.Zipcode {
		t.Errorf("zipcode is %q, want the default %q", staged.Weather.Zipcode, want.Weather.Zipcode)
	}
	if len(staged.Widgets) != len(want.Widgets) {
		t.Errorf("got %d widgets, want the default arrangement's %d",
			len(staged.Widgets), len(want.Widgets))
	}

	// And written down, or the next boot brings the old settings back.
	saved, _, err := config.Load(path)
	if err != nil {
		t.Fatalf("load saved config: %v", err)
	}
	if saved.Hostname != want.Hostname || len(saved.Widgets) != len(want.Widgets) {
		t.Errorf("saved config is not the default: %+v", saved)
	}
}

// A save that cannot reach the disk must leave the running mirror alone, or
// the file and the display disagree until the next reboot silently undoes the
// reset.
func TestResetThatCannotSaveChangesNothing(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	s := testServer(t, filepath.Join(dir, "config.json"))

	w := httptest.NewRecorder()
	s.handleReset(w, post("/api/reset", "application/json"))

	if w.Code == http.StatusOK {
		t.Skip("filesystem allowed the write despite mode 0500 (running as root?)")
	}
	if _, ok := s.applier.Take(); ok {
		t.Error("a failed reset staged a config anyway")
	}
	if s.applier.Current().Hostname != "hallway" {
		t.Error("a failed reset changed the running configuration")
	}
}

// The endpoints that throw something away must not be reachable from a form on
// another site. A cross-origin form can POST here without a preflight, but it
// cannot set this content type — so the content type is the whole check.
func TestDestructiveEndpointsRejectFormPosts(t *testing.T) {
	for _, ct := range []string{
		"application/x-www-form-urlencoded",
		"multipart/form-data",
		"text/plain",
		"",
	} {
		s := testServer(t, filepath.Join(t.TempDir(), "config.json"))
		called := make(chan struct{}, 1)
		s.forgetWiFi = func(context.Context) error { called <- struct{}{}; return nil }

		w := httptest.NewRecorder()
		s.handleReset(w, post("/api/reset", ct))
		if w.Code != http.StatusUnsupportedMediaType {
			t.Errorf("reset with Content-Type %q: status %d, want 415", ct, w.Code)
		}
		if _, ok := s.applier.Take(); ok {
			t.Errorf("reset with Content-Type %q staged a config", ct)
		}

		w = httptest.NewRecorder()
		s.handleForgetWiFi(w, post("/api/forget-wifi", ct))
		if w.Code != http.StatusUnsupportedMediaType {
			t.Errorf("forget-wifi with Content-Type %q: status %d, want 415", ct, w.Code)
		}
		select {
		case <-called:
			t.Errorf("forget-wifi with Content-Type %q dropped the network", ct)
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// A charset parameter is normal and must not turn into a rejection.
func TestJSONWithCharsetIsAccepted(t *testing.T) {
	s := testServer(t, filepath.Join(t.TempDir(), "config.json"))

	w := httptest.NewRecorder()
	s.handleReset(w, post("/api/reset", "application/json; charset=utf-8"))

	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
}

func TestForgetWiFiRepliesBeforeDroppingTheNetwork(t *testing.T) {
	s := testServer(t, filepath.Join(t.TempDir(), "config.json"))
	called := make(chan struct{}, 1)
	s.forgetWiFi = func(context.Context) error { called <- struct{}{}; return nil }

	w := httptest.NewRecorder()
	s.handleForgetWiFi(w, post("/api/forget-wifi", "application/json"))

	// The reply has to be complete by the time the handler returns: the
	// connection it travels over is about to go away.
	if w.Code != http.StatusOK {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	var body struct {
		OK   bool   `json:"ok"`
		SSID string `json:"ssid"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode reply: %v (%s)", err, w.Body)
	}
	if !body.OK {
		t.Error("reply did not confirm the request")
	}
	// The page tells someone which network to look for, so it has to be told
	// which one that is rather than hardcoding a name that could drift.
	if body.SSID == "" {
		t.Error("reply did not name the setup network")
	}

	select {
	case <-called:
	case <-time.After(5 * time.Second):
		t.Fatal("the network was never dropped")
	}
}

// A build with no wifi to manage — a laptop preview, say — must say so rather
// than offer a button that silently does nothing.
func TestForgetWiFiUnsupported(t *testing.T) {
	s := testServer(t, filepath.Join(t.TempDir(), "config.json"))

	w := httptest.NewRecorder()
	s.handleForgetWiFi(w, post("/api/forget-wifi", "application/json"))

	if w.Code != http.StatusNotFound {
		t.Errorf("status %d, want 404", w.Code)
	}
}
