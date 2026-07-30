// Package health owns the watchdog and the "this build is good" marker.
//
// The critical design point is *where* the watchdog gets petted: only from
// the render loop, and nowhere else.
//
// v1 armed a 15-second hardware watchdog at the bottom of a loop that also
// performed synchronous TLS fetches (kernel.cpp:600), so a calendar server
// that accepted a connection and went quiet looked identical to a wedged
// device — and rebooted it, into another hung fetch. Petting from the render
// loop alone makes the two distinguishable: frames stopping is a real wedge,
// a stalled network is not.
package health

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"unsafe"

	"golang.org/x/sys/unix"
)

// Linux watchdog ioctls.
const (
	wdiocSetTimeout = 0xc0045706
	wdiocKeepAlive  = 0x80045705
)

// Monitor pets the hardware watchdog and records process health.
type Monitor struct {
	log      *slog.Logger
	stateDir string

	watchdog *os.File
	started  time.Time

	// healthyAfter is how long the process must run before it declares
	// itself good. It has to outlast the failure modes worth catching —
	// a binary that crashes on startup, or one that comes up and dies as
	// soon as it touches the framebuffer.
	healthyAfter time.Duration
	marked       bool
}

// Options configures a Monitor.
type Options struct {
	// StateDir is where the health marker and failure counter live —
	// the FAT partition, shared with mm-supervise.
	StateDir string

	// WatchdogPath is the device to pet, e.g. /dev/watchdog. Empty
	// disables the hardware watchdog.
	WatchdogPath string

	// WatchdogTimeout is how long the hardware waits before resetting.
	// Generous relative to the render tick: it should fire for a genuine
	// wedge, not for one slow frame.
	WatchdogTimeout time.Duration

	// HealthyAfter defaults to 60s.
	HealthyAfter time.Duration
}

// New returns a Monitor. A missing or unusable watchdog device is logged and
// tolerated: it is a safety net, and the mirror should still run on hardware
// or a laptop that does not have one.
func New(opts Options, log *slog.Logger) *Monitor {
	if opts.HealthyAfter <= 0 {
		opts.HealthyAfter = 60 * time.Second
	}
	if opts.WatchdogTimeout <= 0 {
		opts.WatchdogTimeout = 30 * time.Second
	}

	m := &Monitor{
		log:          log,
		stateDir:     opts.StateDir,
		started:      time.Now(),
		healthyAfter: opts.HealthyAfter,
	}

	if opts.WatchdogPath != "" {
		if err := m.openWatchdog(opts.WatchdogPath, opts.WatchdogTimeout); err != nil {
			log.Warn("hardware watchdog unavailable", "path", opts.WatchdogPath, "err", err)
		} else {
			log.Info("hardware watchdog armed",
				"path", opts.WatchdogPath, "timeout", opts.WatchdogTimeout)
		}
	}
	return m
}

func (m *Monitor) openWatchdog(path string, timeout time.Duration) error {
	f, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		return err
	}

	secs := int32(timeout.Seconds())
	if _, _, errno := unix.Syscall(unix.SYS_IOCTL, f.Fd(),
		uintptr(wdiocSetTimeout), uintptr(unsafeAddr(&secs))); errno != 0 {
		// Not fatal: many watchdogs have a fixed timeout and reject this.
		m.log.Debug("watchdog timeout not settable", "err", errno)
	}

	m.watchdog = f
	return nil
}

// Pet reports that the render loop completed a frame.
//
// Call this from the render goroutine and from nowhere else. Petting it from
// a timer, a fetcher, or a health-check endpoint would recreate exactly the
// v1 failure: a watchdog that stays happy while the screen is frozen.
func (m *Monitor) Pet() {
	if m.watchdog != nil {
		if _, _, errno := unix.Syscall(unix.SYS_IOCTL, m.watchdog.Fd(),
			uintptr(wdiocKeepAlive), 0); errno != 0 {
			// Fall back to the write-based keepalive, which older drivers use.
			if _, err := m.watchdog.Write([]byte("\000")); err != nil {
				m.log.Warn("watchdog keepalive failed", "err", err)
			}
		}
	}

	if !m.marked && time.Since(m.started) >= m.healthyAfter {
		m.markHealthy()
	}
}

// markHealthy records that this binary reached a working state.
//
// mm-supervise increments a failure counter on every start and rolls back to
// the previous binary after three starts that never got here. Clearing the
// counter is therefore the app asserting "I work" — and the only thing that
// stops an automatic revert.
func (m *Monitor) markHealthy() {
	m.marked = true
	if m.stateDir == "" {
		return
	}

	marker := filepath.Join(m.stateDir, "health")
	body := fmt.Sprintf("healthy at %s\n", time.Now().Format(time.RFC3339))
	if err := os.WriteFile(marker, []byte(body), 0o644); err != nil {
		m.log.Warn("could not write health marker", "path", marker, "err", err)
		return
	}

	failures := filepath.Join(m.stateDir, "failures")
	if err := os.WriteFile(failures, []byte("0\n"), 0o644); err != nil {
		m.log.Warn("could not clear failure counter", "path", failures, "err", err)
	}

	m.log.Info("marked healthy", "after", time.Since(m.started).Round(time.Second))
}

// Healthy reports whether this run has been marked good yet.
func (m *Monitor) Healthy() bool { return m.marked }

// Close disarms the watchdog for a controlled shutdown.
//
// The magic 'V' write is the kernel's documented way to say "I am stopping on
// purpose, do not reset". Without it, a clean exit would reboot the device
// one timeout later.
func (m *Monitor) Close() error {
	if m.watchdog == nil {
		return nil
	}
	if _, err := m.watchdog.Write([]byte("V")); err != nil {
		m.log.Warn("could not disarm watchdog", "err", err)
	}
	err := m.watchdog.Close()
	m.watchdog = nil
	return err
}

// unsafeAddr returns the address of v for an ioctl argument.
func unsafeAddr(v *int32) uintptr { return uintptr(unsafe.Pointer(v)) }
