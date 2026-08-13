// Package netmon keeps the WiFi link alive.
//
// v1's WiFiMonitor escalated Healthy -> Degraded -> Kicked -> Dead and then
// asked the kernel to reboot (kernel.cpp:452). Reboot was the *first* real
// recovery tool, because on bare metal there was nothing beneath the app to
// restart. When the reboot did not take, the only remaining option was the
// power cable.
//
// On Linux there are several rungs below that, and this package climbs them
// in order: reassociate, restart the supplicant, reload the driver module,
// and only then reboot. Each rung is strictly more disruptive than the last,
// and success at any point resets to the bottom.
package netmon

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os/exec"
	"sync"
	"time"
)

// Rung is a step on the recovery ladder.
type Rung int

const (
	// RungNone means the link is healthy and nothing needs doing.
	RungNone Rung = iota

	// RungReassociate asks the supplicant to re-run association. Cheap and
	// usually enough for an AP that rebooted.
	RungReassociate

	// RungRestartSupplicant restarts wpa_supplicant, clearing any state it
	// has got itself into.
	RungRestartSupplicant

	// RungReloadDriver removes and reinserts brcmfmac. This hard-resets the
	// radio itself without rebooting the machine, and is the rung v1 had no
	// equivalent of — it went straight from "disassociate" to "reboot".
	RungReloadDriver

	// RungReboot is the last resort.
	RungReboot
)

func (r Rung) String() string {
	switch r {
	case RungReassociate:
		return "reassociate"
	case RungRestartSupplicant:
		return "restart-supplicant"
	case RungReloadDriver:
		return "reload-driver"
	case RungReboot:
		return "reboot"
	default:
		return "healthy"
	}
}

// Status is what the supervisor currently believes about the link.
type Status struct {
	Up            bool
	Rung          Rung
	Failures      int
	LastOK        time.Time
	LastAttempt   time.Time
	LastErr       error
	RecoveryCount int
}

// KeyNetwork is the store key network status is published under.
const KeyNetwork = "network"

// Runner executes a command. Injected so the escalation ladder can be tested
// on a laptop without a radio to break.
type Runner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

// ExecRunner runs real commands.
type ExecRunner struct{}

func (ExecRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)

	// Do not wait on output the child has handed to something it left behind.
	//
	// CombinedOutput reads until the pipes close, and a pipe stays open as
	// long as any process holds it — including grandchildren the child
	// backgrounded and never waited for. S40network is exactly that shape: it
	// starts ntpd with a trailing ampersand, and ntpd inherits these pipes and
	// keeps them for as long as it runs, which is forever.
	//
	// Without this, cancelling the context kills the child and Wait still
	// blocks on the grandchild's copy of the pipe. The recovery ladder calls
	// S40network at the driver-reload rung, so the effect is a supervisor that
	// stops supervising precisely when the network is broken, never reaching
	// the reboot rung and never probing again.
	cmd.WaitDelay = 5 * time.Second

	return cmd.CombinedOutput()
}

// Prober tests whether packets actually route end to end.
type Prober interface {
	Probe(ctx context.Context) error
}

// DialProbe resolves a hostname and opens a TCP connection.
//
// DNS *and* TCP on purpose: a resolvable name proves the resolver is
// reachable, and a completed handshake proves the path is. An ICMP ping
// would prove neither, and plenty of the failure modes here leave ping
// working while TLS does not.
type DialProbe struct {
	Host    string
	Port    string
	Timeout time.Duration
}

func (p DialProbe) Probe(ctx context.Context) error {
	host, port := p.Host, p.Port
	if host == "" {
		host = "one.one.one.one"
	}
	if port == "" {
		port = "443"
	}
	timeout := p.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort(host, port))
	if err != nil {
		return err
	}
	return conn.Close()
}

// Supervisor drives the recovery ladder.
type Supervisor struct {
	// Interface is the wireless interface, e.g. wlan0.
	Interface string

	// Module is the driver to reload at RungReloadDriver.
	Module string

	// Probe tests connectivity.
	Probe Prober

	// Runner executes recovery commands.
	Runner Runner

	// Reboot performs the last-resort reboot. Injected so tests can observe
	// it without rebooting the test host.
	Reboot func(ctx context.Context) error

	// Interval between probes when healthy.
	Interval time.Duration

	// FailuresPerRung is how many consecutive probe failures are tolerated
	// before climbing. Escalating on a single failure would mean every
	// transient blip restarts the supplicant.
	FailuresPerRung int

	// Settle is how long to wait after a recovery action before probing
	// again — reassociation and DHCP both need a moment.
	Settle time.Duration

	// CommandTimeout bounds one recovery command.
	//
	// Every rung shells out, and the things it shells out to talk to a radio
	// driver that is already misbehaving — that is why the ladder is running.
	// A wpa_cli or modprobe that never returns would block the ladder inside
	// act, so the supervisor stops probing, never climbs to the rung that
	// would have fixed it, and never reboots. Nothing else is watching: the
	// hardware watchdog is fed by the render loop, which carries on happily
	// drawing a clock above a dead network.
	//
	// Defaults to 30s, which is long next to any of these commands.
	CommandTimeout time.Duration

	// ActionDelay is the pause between steps within a single recovery
	// action, e.g. between rmmod and modprobe. Configurable rather than a
	// hardcoded sleep so tests do not have to wait out real seconds, and so
	// a shutdown is never stuck behind one.
	ActionDelay time.Duration

	Log *slog.Logger

	// OnStatus is called whenever status changes, for publishing to the
	// store. Never called from the render goroutine.
	OnStatus func(Status)

	// Suspended reports that recovery should not run at all right now —
	// used while the setup portal owns the radio, where "no connectivity"
	// is the expected state rather than a fault.
	Suspended func() bool

	mu     sync.Mutex
	status Status

	// everUp records whether a probe has ever succeeded.
	//
	// This gates the reboot rung, and the reason is a bug found on real
	// hardware: a device with no wifi credentials climbed the whole ladder
	// and rebooted, every six minutes, forever. Rebooting is a way to
	// recover something that *was* working. If it has never worked, there
	// is nothing to recover to and a reboot is just a loop — exactly the
	// failure this package exists to prevent.
	everUp bool
}

// New returns a Supervisor with workable defaults.
func New(iface string, log *slog.Logger) *Supervisor {
	return &Supervisor{
		Interface:       iface,
		Module:          "brcmfmac",
		Probe:           DialProbe{},
		Runner:          ExecRunner{},
		Reboot:          defaultReboot,
		Interval:        30 * time.Second,
		CommandTimeout:  30 * time.Second,
		FailuresPerRung: 3,
		Settle:          15 * time.Second,
		ActionDelay:     2 * time.Second,
		Log:             log,
	}
}

// pause waits for d, or returns early if ctx is cancelled.
//
// Every wait in this package goes through here. A plain time.Sleep inside a
// recovery action would make shutdown block on it, which is a poor trait for
// the component whose whole job is recovering from things being stuck.
func (s *Supervisor) pause(ctx context.Context, d time.Duration) {
	if d <= 0 {
		return
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

// run executes one recovery command under its own deadline, so a command that
// hangs cannot take the ladder with it.
func (s *Supervisor) run(ctx context.Context, name string, args ...string) ([]byte, error) {
	timeout := s.CommandTimeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return s.Runner.Run(cctx, name, args...)
}

func (s *Supervisor) actionDelay() time.Duration {
	if s.ActionDelay <= 0 {
		return 2 * time.Second
	}
	return s.ActionDelay
}

// Status returns the current link status.
func (s *Supervisor) Status() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.status
}

func (s *Supervisor) setStatus(f func(*Status)) {
	s.mu.Lock()
	f(&s.status)
	snapshot := s.status
	s.mu.Unlock()

	if s.OnStatus != nil {
		s.OnStatus(snapshot)
	}
}

// Run drives the ladder until ctx is cancelled.
func (s *Supervisor) Run(ctx context.Context) {
	if s.Interval <= 0 {
		s.Interval = 30 * time.Second
	}
	if s.FailuresPerRung <= 0 {
		s.FailuresPerRung = 3
	}

	for {
		// While the setup portal owns the radio there is deliberately no
		// route to anywhere, so probing would escalate against a state that
		// is working as intended.
		if s.Suspended != nil && s.Suspended() {
			select {
			case <-ctx.Done():
				return
			case <-time.After(s.Interval):
			}
			continue
		}

		err := s.Probe.Probe(ctx)
		now := time.Now()

		if err == nil {
			s.everUp = true
			s.setStatus(func(st *Status) {
				st.Up = true
				st.LastOK = now
				st.LastAttempt = now
				st.LastErr = nil
				// Success resets to the bottom of the ladder. A link that
				// recovers should not stay one failure away from a reboot.
				if st.Rung != RungNone {
					s.Log.Info("link recovered", "was", st.Rung.String())
				}
				st.Rung = RungNone
				st.Failures = 0
			})
		} else {
			if ctx.Err() != nil {
				return
			}
			s.handleFailure(ctx, err, now)
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(s.Interval):
		}
	}
}

func (s *Supervisor) handleFailure(ctx context.Context, probeErr error, now time.Time) {
	var climb bool

	s.setStatus(func(st *Status) {
		st.Up = false
		st.LastAttempt = now
		st.LastErr = probeErr
		st.Failures++
		climb = st.Failures%s.FailuresPerRung == 0
	})

	if !climb {
		s.Log.Debug("probe failed", "err", probeErr, "failures", s.Status().Failures)
		return
	}

	// The ceiling is the reboot rung only for a link that has worked before.
	// A device that has never associated has nothing to reboot back into.
	ceiling := RungReboot
	if !s.everUp {
		ceiling = RungReloadDriver
	}

	var next Rung
	var atCeiling bool
	s.setStatus(func(st *Status) {
		if st.Rung < ceiling {
			st.Rung++
		} else {
			atCeiling = true
		}
		st.RecoveryCount++
		next = st.Rung
	})

	if atCeiling && !s.everUp {
		// Stay here rather than rebooting. The setup portal is the right
		// answer for a device that has never been on a network, and it is
		// running in parallel.
		s.Log.Warn("link has never come up; holding at the top of the ladder rather than rebooting",
			"rung", next.String(), "failures", s.Status().Failures)
		return
	}

	s.Log.Warn("link down; escalating",
		"rung", next.String(), "err", probeErr, "failures", s.Status().Failures)

	if err := s.act(ctx, next); err != nil {
		s.Log.Error("recovery action failed", "rung", next.String(), "err", err)
	}

	// Give the action time to take effect before the next probe, otherwise
	// we would climb again immediately on a link that is still coming up.
	select {
	case <-ctx.Done():
	case <-time.After(s.Settle):
	}
}

// act performs the recovery for a rung.
func (s *Supervisor) act(ctx context.Context, rung Rung) error {
	switch rung {
	case RungReassociate:
		_, err := s.run(ctx, "wpa_cli", "-i", s.Interface, "reassociate")
		return err

	case RungRestartSupplicant:
		// Ignore the stop error: the supplicant may already be dead, which
		// is itself a reason we are here.
		_, _ = s.run(ctx, "killall", "wpa_supplicant")
		s.pause(ctx, s.actionDelay()/2)
		if _, err := s.run(ctx, "wpa_supplicant", "-B",
			"-i", s.Interface, "-c", "/var/run/wpa_supplicant.conf", "-D", "nl80211"); err != nil {
			return err
		}
		_, err := s.run(ctx, "udhcpc", "-b", "-i", s.Interface, "-t", "5", "-T", "3")
		return err

	case RungReloadDriver:
		// The rung that makes a reboot unnecessary in most cases: this
		// hard-resets the radio without touching anything else.
		_, _ = s.run(ctx, "killall", "wpa_supplicant")
		// rmmod failing is not a reason to skip modprobe: the module may
		// already be unloaded, which is itself a plausible reason the radio
		// is missing. Pressing on is more likely to help than bailing.
		if _, err := s.run(ctx, "rmmod", s.Module); err != nil {
			s.Log.Warn("rmmod failed; attempting modprobe anyway",
				"module", s.Module, "err", err)
		}
		s.pause(ctx, s.actionDelay())
		if _, err := s.run(ctx, "modprobe", s.Module); err != nil {
			return fmt.Errorf("modprobe %s: %w", s.Module, err)
		}
		s.pause(ctx, s.actionDelay())
		_, err := s.run(ctx, "/etc/init.d/S40network", "start")
		return err

	case RungReboot:
		s.Log.Error("exhausted recovery ladder; rebooting")
		if s.Reboot == nil {
			return fmt.Errorf("no reboot function configured")
		}
		return s.Reboot(ctx)

	default:
		return nil
	}
}

func defaultReboot(ctx context.Context) error {
	return exec.CommandContext(ctx, "reboot").Run()
}
