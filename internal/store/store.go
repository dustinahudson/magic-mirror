// Package store holds fetched data and hands it to the renderer without ever
// blocking it.
//
// This is the load-bearing package for the bug that motivated the rewrite. In
// v1, src/core/kernel.cpp called FetchCalendar (line 489) and the LVGL render
// tick (line 577) from the same loop body, so a slow calendar server stopped
// the clock. Here, fetchers publish immutable snapshots and the render loop
// does a single atomic pointer load. There is no lock the renderer can block
// on and no I/O it can perform.
package store

import (
	"maps"
	"sync"
	"sync/atomic"
	"time"
)

// Status describes what is known about a value.
//
// This type exists because of v1's other bug: weather_widget.cpp:38-48 seeded
// a complete fake reading (72°F, "Partly Cloudy", Dallas) so a failed fetch
// rendered as confident, plausible, invented weather. A Go zero value would
// do exactly the same thing, so readings never expose a bare struct — you
// must go through Get, which forces you to consider Status.
type Status int

const (
	// Never means no fetch has completed yet. Boot state. Renders as
	// placeholder chrome, never as placeholder data.
	Never Status = iota

	// Fresh means the most recent fetch succeeded within its freshness window.
	Fresh

	// Stale means we hold real data from an earlier success, but the most
	// recent attempt failed or the data has aged out. Shown, with a marker.
	Stale

	// Failed means we have never held real data and the last attempt failed.
	Failed
)

func (s Status) String() string {
	switch s {
	case Fresh:
		return "fresh"
	case Stale:
		return "stale"
	case Failed:
		return "failed"
	default:
		return "never"
	}
}

// HasValue reports whether a reading in this state carries real data.
func (s Status) HasValue() bool { return s == Fresh || s == Stale }

// Entry is one source's data plus everything known about its health.
type Entry struct {
	value any

	Status      Status
	LastSuccess time.Time
	LastAttempt time.Time
	LastErr     error
}

// Age is how long ago this entry last held a successful fetch. Computed at
// read time rather than stored, so a snapshot held across several frames
// cannot report a frozen age.
func (e Entry) Age(now time.Time) time.Duration {
	if e.LastSuccess.IsZero() {
		return 0
	}
	return now.Sub(e.LastSuccess)
}

// Reading is a typed view of an Entry.
type Reading[T any] struct {
	Entry
	value T
}

// Get returns the value and whether it is real data.
//
// The bool is not decoration. There is deliberately no way to reach the value
// without handling the case where there isn't one.
func (r Reading[T]) Get() (T, bool) {
	if !r.Status.HasValue() {
		var zero T
		return zero, false
	}
	return r.value, true
}

// Or returns the value, or fallback when there is none. For genuinely
// cosmetic cases only — never for data the viewer would read as a
// measurement.
func (r Reading[T]) Or(fallback T) T {
	if v, ok := r.Get(); ok {
		return v
	}
	return fallback
}

// Snapshot is an immutable view of every source at one instant.
type Snapshot struct {
	at      time.Time
	entries map[string]Entry
}

// At is the time this snapshot was published.
func (s *Snapshot) At() time.Time { return s.at }

// Keys lists every source present, for diagnostics and the web UI.
func (s *Snapshot) Keys() []string {
	if s == nil {
		return nil
	}
	out := make([]string, 0, len(s.entries))
	for k := range s.entries {
		out = append(out, k)
	}
	return out
}

// Entry returns the untyped entry for key.
func (s *Snapshot) Entry(key string) (Entry, bool) {
	if s == nil {
		return Entry{}, false
	}
	e, ok := s.entries[key]
	return e, ok
}

// Get reads a typed reading out of a snapshot.
//
// A missing key or a type mismatch both yield Never rather than an error: a
// widget asking for data nobody publishes should render its empty state, not
// take down the render loop. Type mismatch can only happen if two sources
// claim one key, which config validation rejects.
func Get[T any](s *Snapshot, key string) Reading[T] {
	if s == nil {
		return Reading[T]{}
	}
	e, ok := s.entries[key]
	if !ok {
		return Reading[T]{}
	}
	r := Reading[T]{Entry: e}
	if e.Status.HasValue() {
		v, ok := e.value.(T)
		if !ok {
			r.Entry.Status = Never
			return r
		}
		r.value = v
	}
	return r
}

// Store is the publish side. Fetchers write here; the renderer only ever
// calls Load.
type Store struct {
	cur atomic.Pointer[Snapshot]

	// mu serialises publishers only. The renderer never touches it.
	mu   sync.Mutex
	now  func() time.Time
	ttls map[string]time.Duration
}

// New returns an empty Store.
func New() *Store {
	s := &Store{now: time.Now, ttls: make(map[string]time.Duration)}
	s.cur.Store(&Snapshot{at: time.Now(), entries: map[string]Entry{}})
	return s
}

// SetClock overrides the time source. Tests only.
func (s *Store) SetClock(now func() time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.now = now
}

// SetTTL declares how long a key's data stays Fresh before it degrades to
// Stale. Zero means it never ages out on its own.
func (s *Store) SetTTL(key string, ttl time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ttls[key] = ttl
}

// Load returns the current snapshot.
//
// A single atomic pointer load — wait-free, allocation-free, and the only
// thing the render loop needs from this package.
func (s *Store) Load() *Snapshot {
	snap := s.cur.Load()
	if snap == nil {
		return &Snapshot{entries: map[string]Entry{}}
	}
	return snap
}

// Success records a completed fetch.
func (s *Store) Success(key string, value any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	s.publish(key, Entry{
		value:       value,
		Status:      Fresh,
		LastSuccess: now,
		LastAttempt: now,
	})
}

// Failure records a failed fetch. Any previously held value is kept and
// demoted to Stale; a source that has never succeeded becomes Failed.
//
// This is the whole difference from v1, where a failed fetch either left fake
// defaults on screen or, if it hung, tripped the watchdog into a reboot.
func (s *Store) Failure(key string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()

	prev, existed := s.Load().entries[key]
	next := Entry{
		Status:      Failed,
		LastAttempt: now,
		LastErr:     err,
	}
	if existed && prev.Status.HasValue() {
		next.value = prev.value
		next.Status = Stale
		next.LastSuccess = prev.LastSuccess
	}
	s.publish(key, next)
}

// Attempt records that a fetch has started, without changing the value. Lets
// the UI show "refreshing" without implying anything about the result.
func (s *Store) Attempt(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()

	prev := s.Load().entries[key]
	prev.LastAttempt = now
	s.publish(key, prev)
}

// publish swaps in a new snapshot. Caller holds mu.
//
// The map is copied on every publish so readers hold a genuinely immutable
// value. Maps here have single-digit key counts and publishes happen on the
// order of minutes, so the copy is irrelevant next to the guarantee it buys.
func (s *Store) publish(key string, e Entry) {
	old := s.Load()
	entries := make(map[string]Entry, len(old.entries)+1)
	maps.Copy(entries, old.entries)
	entries[key] = e

	now := s.now()
	// Age out anything past its TTL as we go, so staleness appears without
	// needing a background sweeper.
	for k, v := range entries {
		ttl, ok := s.ttls[k]
		if !ok || ttl <= 0 || !v.Status.HasValue() {
			continue
		}
		if now.Sub(v.LastSuccess) > ttl {
			v.Status = Stale
			entries[k] = v
		}
	}

	s.cur.Store(&Snapshot{at: now, entries: entries})
}

// Refresh republishes the current snapshot so TTL-based staleness is
// re-evaluated. Cheap enough to call once a second from the render loop's
// ticker goroutine — but never from the render loop itself.
func (s *Store) Refresh() {
	s.mu.Lock()
	defer s.mu.Unlock()
	old := s.Load()
	entries := make(map[string]Entry, len(old.entries))
	maps.Copy(entries, old.entries)

	now := s.now()
	for k, v := range entries {
		ttl, ok := s.ttls[k]
		if !ok || ttl <= 0 || v.Status != Fresh {
			continue
		}
		if now.Sub(v.LastSuccess) > ttl {
			v.Status = Stale
			entries[k] = v
		}
	}
	s.cur.Store(&Snapshot{at: now, entries: entries})
}
