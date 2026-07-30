// Package source fetches data and publishes it into the store.
//
// Every fetcher runs on its own goroutine with a hard context deadline, and
// every failure is a state rather than a fatal. Nothing here can reach the
// framebuffer, and nothing the renderer does can block a fetch.
//
// This is the half of the design that fixes v1's boot loop. There, a hung API
// blocked the shared loop past the 15s watchdog window (kernel.cpp:600) and
// the device rebooted into another hung fetch. Here a hung API produces a
// timeout, a logged error, a Stale reading, and a backed-off retry.
package source

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/dustinahudson/magic-mirror/internal/store"
)

// Fetcher produces one source's data.
//
// Implementations must respect ctx: it carries the deadline that keeps a
// dead server from becoming an indefinite hang.
type Fetcher interface {
	// Key names this source in the store.
	Key() string

	// Interval is how often to fetch on the happy path.
	Interval() time.Duration

	// Timeout bounds a single attempt.
	Timeout() time.Duration

	// Fetch performs one attempt.
	Fetch(ctx context.Context) (any, error)
}

// Manager runs fetchers and publishes their results.
type Manager struct {
	store *store.Store
	log   *slog.Logger

	mu       sync.Mutex
	fetchers []Fetcher

	// now and after are injectable so tests can drive time without sleeping.
	now   func() time.Time
	after func(time.Duration) <-chan time.Time
}

// NewManager returns a Manager publishing into s.
func NewManager(s *store.Store, log *slog.Logger) *Manager {
	return &Manager{
		store: s,
		log:   log,
		now:   time.Now,
		after: time.After,
	}
}

// Add registers a fetcher. Must be called before Run.
func (m *Manager) Add(f Fetcher) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.fetchers = append(m.fetchers, f)
	if f.Interval() > 0 {
		// Data older than three intervals is stale even if no fetch has
		// failed — covers a fetcher goroutine that somehow stops running.
		m.store.SetTTL(f.Key(), 3*f.Interval())
	}
}

// Run drives every fetcher until ctx is cancelled, returning when they have
// all stopped.
func (m *Manager) Run(ctx context.Context) {
	m.mu.Lock()
	fetchers := append([]Fetcher(nil), m.fetchers...)
	m.mu.Unlock()

	var wg sync.WaitGroup
	for _, f := range fetchers {
		wg.Add(1)
		go func(f Fetcher) {
			defer wg.Done()
			m.runOne(ctx, f)
		}(f)
	}
	wg.Wait()
}

// runOne is a single fetcher's lifetime: fetch, publish, wait, repeat.
//
// On failure it backs off exponentially with jitter rather than hammering a
// struggling server — and, crucially, it just keeps going. There is no error
// path here that stops the mirror, reboots the device, or touches the
// display.
func (m *Manager) runOne(ctx context.Context, f Fetcher) {
	const (
		minBackoff = 15 * time.Second
		maxBackoff = 10 * time.Minute
	)

	key := f.Key()
	backoff := minBackoff
	log := m.log.With("source", key)

	for {
		m.store.Attempt(key)

		attemptCtx, cancel := context.WithTimeout(ctx, f.Timeout())
		started := m.now()
		value, err := f.Fetch(attemptCtx)
		cancel()
		took := m.now().Sub(started)

		var wait time.Duration
		switch {
		case err == nil:
			m.store.Success(key, value)
			log.Debug("fetch ok", "took", took)
			backoff = minBackoff
			wait = f.Interval()

		case ctx.Err() != nil:
			// Shutting down, not a source failure.
			return

		default:
			m.store.Failure(key, err)
			// A timeout is worth distinguishing in the log: it is the exact
			// condition that used to reboot the device.
			if errors.Is(err, context.DeadlineExceeded) {
				log.Warn("fetch timed out", "after", took, "retry_in", backoff)
			} else {
				log.Warn("fetch failed", "err", err, "took", took, "retry_in", backoff)
			}
			wait = backoff
			backoff = min(backoff*2, maxBackoff)
		}

		select {
		case <-ctx.Done():
			return
		case <-m.after(jitter(wait)):
		}
	}
}

// jitter spreads retries by ±20% so several failing sources do not
// synchronise into a thundering herd against the same recovering server.
func jitter(d time.Duration) time.Duration {
	if d <= 0 {
		return time.Second
	}
	delta := float64(d) * 0.2
	return time.Duration(float64(d) - delta + rand.Float64()*2*delta)
}

// HTTPClient returns the client every fetcher should use.
//
// Timeouts are set at every layer rather than relying on the request context
// alone: a connection that opens and then stalls mid-body is exactly the
// shape of failure that hung v1, and Timeout covers the whole exchange
// including the body read.
func HTTPClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			DialContext: (&net.Dialer{
				Timeout:   10 * time.Second,
				KeepAlive: 30 * time.Second,
			}).DialContext,
			TLSHandshakeTimeout:   10 * time.Second,
			ResponseHeaderTimeout: 15 * time.Second,
			ExpectContinueTimeout: 2 * time.Second,
			IdleConnTimeout:       90 * time.Second,
			MaxIdleConns:          4,
			// A Pi Zero W has one core and 512MB; there is no value in
			// holding many idle connections open.
			MaxIdleConnsPerHost: 2,
		},
	}
}

// ErrHTTPStatus reports a non-2xx response.
type ErrHTTPStatus struct {
	URL    string
	Status int
}

func (e *ErrHTTPStatus) Error() string {
	return fmt.Sprintf("%s: HTTP %d", e.URL, e.Status)
}
