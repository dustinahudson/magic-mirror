package web

import (
	"sync/atomic"

	"github.com/dustinahudson/magic-mirror/internal/config"
)

// Applier hands a new configuration from the web server to the render loop.
//
// The web server must never touch the compositor. Widgets are built and
// placed by the render goroutine, and a config save arriving on an HTTP
// goroutine has no business mutating that. So a save stages a config here,
// and the render loop takes it at the top of a tick — the same
// pointer-swap discipline the data snapshots use.
type Applier struct {
	pending atomic.Pointer[config.Config]

	// current is what the mirror is actually running. It is only replaced
	// once the render loop has accepted a staged config, so a config that
	// fails to apply never becomes "current".
	current atomic.Pointer[config.Config]
}

// NewApplier returns an Applier seeded with the running configuration.
func NewApplier(cfg config.Config) *Applier {
	a := &Applier{}
	a.current.Store(&cfg)
	return a
}

// Stage queues a configuration for the render loop to pick up.
func (a *Applier) Stage(cfg config.Config) { a.pending.Store(&cfg) }

// Take returns a staged configuration, if any, and clears it.
//
// Called from the render goroutine only.
func (a *Applier) Take() (config.Config, bool) {
	p := a.pending.Swap(nil)
	if p == nil {
		return config.Config{}, false
	}
	a.current.Store(p)
	return *p, true
}

// Current returns the running configuration.
func (a *Applier) Current() config.Config {
	if c := a.current.Load(); c != nil {
		return *c
	}
	return config.Default()
}
