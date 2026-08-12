package source

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/dustinahudson/magic-mirror/internal/store"
)

// probe is a fetcher that reports when it was first asked to fetch.
type probe struct {
	key       string
	networked bool
	fetched   chan struct{}
}

func newProbe(key string, networked bool) *probe {
	return &probe{key: key, networked: networked, fetched: make(chan struct{}, 1)}
}

func (p *probe) Key() string             { return p.key }
func (p *probe) Interval() time.Duration { return time.Hour }
func (p *probe) Timeout() time.Duration  { return time.Second }

func (p *probe) Fetch(ctx context.Context) (any, error) {
	select {
	case p.fetched <- struct{}{}:
	default:
	}
	return "value", nil
}

// NeedsNetwork is declared on the concrete type, so a probe built with
// networked=false still satisfies the optional interface — which is the
// realistic shape, since a source knows statically what it needs.
func (p *probe) NeedsNetwork() bool { return p.networked }

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// waitFetch reports whether the fetcher ran within a generous real-time
// window. The window only has to outlast goroutine scheduling; nothing here
// sleeps for it on the happy path.
func waitFetch(t *testing.T, p *probe) bool {
	t.Helper()
	select {
	case <-p.fetched:
		return true
	case <-time.After(2 * time.Second):
		return false
	}
}

func didNotFetch(t *testing.T, p *probe) bool {
	t.Helper()
	select {
	case <-p.fetched:
		return false
	case <-time.After(150 * time.Millisecond):
		return true
	}
}

// The bug this exists for: fetching before the radio has associated does not
// just fail once, it doubles the backoff each time, so the first calendar
// lands minutes after the network was actually usable.
func TestNetworkedFetcherWaitsForTheLink(t *testing.T) {
	ready := make(chan struct{})
	m := NewManager(store.New(), quiet())
	m.WaitForNetwork(ready, time.Hour) // grace long enough to be irrelevant

	p := newProbe(KeyWeather, true)
	m.Add(p)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go m.Run(ctx)

	if !didNotFetch(t, p) {
		t.Fatal("fetched before the link was reported up")
	}

	close(ready)

	if !waitFetch(t, p) {
		t.Fatal("did not fetch once the link came up")
	}
}

// A source that works offline must not be held back. The system source is
// what puts the mirror's IP on screen, and that is the reading someone needs
// precisely while there is no network yet.
func TestOfflineFetcherIgnoresTheGate(t *testing.T) {
	m := NewManager(store.New(), quiet())
	m.WaitForNetwork(make(chan struct{}), time.Hour) // never closed

	p := newProbe(KeySystem, false)
	m.Add(p)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go m.Run(ctx)

	if !waitFetch(t, p) {
		t.Fatal("an offline source was blocked by the network gate")
	}
}

// If the link never comes up, or whatever reports on it is wrong, fetching
// late still beats never fetching.
func TestGraceExpiryFetchesAnyway(t *testing.T) {
	m := NewManager(store.New(), quiet())
	m.WaitForNetwork(make(chan struct{}), time.Hour) // never closed

	// Fire the grace timer immediately instead of waiting an hour.
	expired := make(chan time.Time, 1)
	expired <- time.Now()
	m.after = func(time.Duration) <-chan time.Time { return expired }

	p := newProbe(KeyCalendar, true)
	m.Add(p)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go m.Run(ctx)

	if !waitFetch(t, p) {
		t.Fatal("did not fetch after the grace period expired")
	}
}

// Unconfigured means unchanged: a preview run on a laptop has no supervisor
// to ask and must behave exactly as it did before.
func TestNoGateFetchesImmediately(t *testing.T) {
	m := NewManager(store.New(), quiet())

	p := newProbe(KeyWeather, true)
	m.Add(p)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go m.Run(ctx)

	if !waitFetch(t, p) {
		t.Fatal("fetch was delayed with no gate configured")
	}
}

// Cancelling while gated must stop the fetcher rather than leak it until the
// grace period elapses.
func TestCancelWhileWaitingStops(t *testing.T) {
	m := NewManager(store.New(), quiet())
	m.WaitForNetwork(make(chan struct{}), time.Hour)

	p := newProbe(KeyWeather, true)
	m.Add(p)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { m.Run(ctx); close(done) }()

	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after cancellation while gated")
	}
	select {
	case <-p.fetched:
		t.Error("fetched despite being cancelled while gated")
	default:
	}
}
