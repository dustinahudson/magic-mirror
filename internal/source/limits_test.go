package source

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The device has 512MB, no swap, and fetches from whatever the network hands
// it. An endless response body is not a hypothetical: a captive portal that
// answers every request with a page, or a proxy that never closes, produces
// exactly this. The kernel kills the process, mm-supervise counts a failure,
// and three of those roll the mirror back to a build with the same bug.

// endlessBody writes forever, as fast as it is read.
func endlessServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		chunk := strings.Repeat("a", 64<<10)
		for {
			if _, err := w.Write([]byte(chunk)); err != nil {
				return // client gave up, which is the point
			}
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestGetJSONRefusesAnEndlessBody(t *testing.T) {
	srv := endlessServer(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var out map[string]any
	done := make(chan error, 1)
	go func() {
		done <- getJSON(ctx, HTTPClient(10*time.Second), srv.URL, &out)
	}()

	select {
	case err := <-done:
		// It must fail, and it must fail because the body ran out at the
		// limit rather than because it read the whole internet.
		if err == nil {
			t.Fatal("accepted an endless body as valid JSON")
		}
	case <-time.After(30 * time.Second):
		t.Fatal("still reading after 30s; the body is not bounded")
	}
}

// The bound must be generous enough for a real payload. A forecast is a few
// kilobytes; a limit that clipped one would be worse than no limit at all,
// because the mirror would show no weather and blame the API.
func TestGetJSONAcceptsANormalPayload(t *testing.T) {
	body := `{"latitude": 39.05, "longitude": -94.59, "note": "` +
		strings.Repeat("x", 200<<10) + `"}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)

	var out struct {
		Latitude float64 `json:"latitude"`
	}
	if err := getJSON(context.Background(), HTTPClient(10*time.Second), srv.URL, &out); err != nil {
		t.Fatalf("rejected a 200KB payload: %v", err)
	}
	if out.Latitude != 39.05 {
		t.Errorf("latitude = %v, want 39.05", out.Latitude)
	}
}
