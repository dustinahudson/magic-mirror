package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dustinahudson/magic-mirror/internal/store"
)

// /api/logs and /api/status are the only way anyone sees inside this device
// without taking it off a wall and carrying the SD card home. They had no
// tests, which made the fallback for every other failure itself untested.

func logServer(t *testing.T) (*Server, string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "logs"), 0o755); err != nil {
		t.Fatal(err)
	}
	s := testServer(t, filepath.Join(dir, "config.json"))
	s.stateDir = dir
	return s, dir
}

func getLogs(t *testing.T, s *Server, query string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	s.handleLogs(w, httptest.NewRequest(http.MethodGet, "/api/logs"+query, nil))
	return w
}

func TestLogsServeTheApplicationLogByDefault(t *testing.T) {
	s, dir := logServer(t)
	body := "starting mm.current (attempt 1)\nparse config: EOF\n"
	if err := os.WriteFile(filepath.Join(dir, "logs", "mm.log"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	w := getLogs(t, s, "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if got := w.Body.String(); got != body {
		t.Errorf("body = %q, want %q", got, body)
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("Content-Type = %q, want text/plain", ct)
	}
	// A cached log is a log that describes a state the device has left.
	if cc := w.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", cc)
	}
}

func TestLogsServeTheNetworkLogOnRequest(t *testing.T) {
	s, dir := logServer(t)
	body := "no /boot/wpa_supplicant.conf; leaving wlan0 up for provisioning\n"
	if err := os.WriteFile(filepath.Join(dir, "logs", "network.log"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	w := getLogs(t, s, "?file=network.log")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if got := w.Body.String(); got != body {
		t.Errorf("body = %q, want the network log", got)
	}
}

// The endpoint reads files off the device. It must never become a way to read
// any file off the device.
func TestLogsRefuseAnythingOutsideTheKnownSet(t *testing.T) {
	s, dir := logServer(t)
	secret := filepath.Join(dir, "wpa_supplicant.conf")
	if err := os.WriteFile(secret, []byte("psk=\"hunter2\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, name := range []string{
		"wpa_supplicant.conf",
		"../wpa_supplicant.conf",
		"logs/../wpa_supplicant.conf",
		"/etc/passwd",
		"../../../../etc/passwd",
		"config.json",
		"mm.log\x00",
	} {
		w := getLogs(t, s, "?file="+url.QueryEscape(name))
		if w.Code == http.StatusOK {
			t.Errorf("file=%q was served (status 200): %q", name, w.Body.String())
		}
		if strings.Contains(w.Body.String(), "hunter2") {
			t.Errorf("file=%q leaked the wifi password", name)
		}
	}
}

// A log big enough to matter is a log too big to send whole to a phone on the
// same wifi as the device.
func TestLogsAreTailedNotSentWhole(t *testing.T) {
	s, dir := logServer(t)
	const maxBytes = 256 << 10
	head := strings.Repeat("a", maxBytes)
	tail := "THE-INTERESTING-PART-AT-THE-END\n"
	if err := os.WriteFile(filepath.Join(dir, "logs", "mm.log"),
		[]byte(head+tail), 0o644); err != nil {
		t.Fatal(err)
	}

	w := getLogs(t, s, "")
	body := w.Body.String()
	if len(body) > maxBytes {
		t.Errorf("served %d bytes, want at most %d", len(body), maxBytes)
	}
	// Tailed, not truncated: the end is what explains a crash.
	if !strings.HasSuffix(body, tail) {
		t.Error("the end of the log was cut off; the tail is the part that matters")
	}
}

func TestLogsReportAMissingFileRatherThanEmptySuccess(t *testing.T) {
	s, _ := logServer(t)

	w := getLogs(t, s, "")
	if w.Code == http.StatusOK {
		t.Errorf("a missing log returned 200 and %q, which reads as 'nothing wrong'",
			w.Body.String())
	}
}

func TestLogsWithoutAStateDirectory(t *testing.T) {
	s := testServer(t, "")
	w := getLogs(t, s, "")
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 when there is no state directory", w.Code)
	}
}

// Status is the first thing to look at remotely: what the mirror is running,
// and which of its sources are failing and why.
func TestStatusReportsVersionAndSourceFailures(t *testing.T) {
	s, _ := logServer(t)
	s.version = "v0.15.1-rc.4"
	s.data.Success("weather", "sunny")
	s.data.Failure("calendar", errFetch("all 2 calendar feeds failed"))

	w := httptest.NewRecorder()
	s.handleStatus(w, httptest.NewRequest(http.MethodGet, "/api/status", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var got struct {
		Version string `json:"version"`
		Sources []struct {
			Key    string `json:"key"`
			Status string `json:"status"`
			Error  string `json:"error"`
			Age    string `json:"age"`
		} `json:"sources"`
	}
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}

	if got.Version != "v0.15.1-rc.4" {
		t.Errorf("version = %q, want the running build", got.Version)
	}

	byKey := map[string]string{}
	for _, src := range got.Sources {
		byKey[src.Key] = src.Error
	}
	if _, ok := byKey["weather"]; !ok {
		t.Error("a healthy source is missing from status")
	}
	// The reason matters more than the fact. "calendar: failing" sends
	// somebody to the house; the error tells them whether they need to go.
	if !strings.Contains(byKey["calendar"], "calendar feeds failed") {
		t.Errorf("calendar error = %q, want the underlying reason", byKey["calendar"])
	}
}

// An empty store must render as valid JSON, not a null that a phone browser
// shows as a blank page.
func TestStatusWithNoSourcesIsStillValidJSON(t *testing.T) {
	s := testServer(t, "")
	s.data = store.New()

	w := httptest.NewRecorder()
	s.handleStatus(w, httptest.NewRequest(http.MethodGet, "/api/status", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var any map[string]any
	if err := json.NewDecoder(w.Body).Decode(&any); err != nil {
		t.Fatalf("status is not valid JSON: %v", err)
	}
}

type errFetch string

func (e errFetch) Error() string { return string(e) }
