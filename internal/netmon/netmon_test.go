package netmon

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeRunner records commands instead of running them.
type fakeRunner struct {
	mu   sync.Mutex
	cmds []string
	fail map[string]error
}

func newFakeRunner() *fakeRunner {
	return &fakeRunner{fail: map[string]error{}}
}

func (f *fakeRunner) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cmds = append(f.cmds, name+" "+strings.Join(args, " "))
	return nil, f.fail[name]
}

func (f *fakeRunner) commands() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.cmds...)
}

func (f *fakeRunner) ran(substr string) bool {
	for _, c := range f.commands() {
		if strings.Contains(c, substr) {
			return true
		}
	}
	return false
}

// alwaysFailProbe never succeeds, modelling a device that has no
// credentials at all.
type alwaysFailProbe struct{}

func (alwaysFailProbe) Probe(context.Context) error { return errors.New("no link") }

// scriptedProbe fails a fixed number of times, then succeeds.
type scriptedProbe struct {
	mu        sync.Mutex
	failFirst int
	calls     int
}

func (p *scriptedProbe) Probe(context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	if p.calls <= p.failFirst {
		return errors.New("probe failed")
	}
	return nil
}

// The reboot flag is written from the supervisor's goroutine and read from
// the test's, so it has to be atomic. A plain bool here raced, which made
// -race unreliable for the package that decides whether a wedged device
// recovers on its own or waits for someone to drive over.
func testSupervisor(probe Prober, runner Runner) (*Supervisor, *atomic.Bool) {
	var rebooted atomic.Bool
	s := &Supervisor{
		Interface:       "wlan0",
		Module:          "brcmfmac",
		Probe:           probe,
		Runner:          runner,
		Interval:        time.Millisecond,
		Settle:          time.Millisecond,
		ActionDelay:     time.Millisecond,
		FailuresPerRung: 2,
		Log:             slog.New(slog.NewTextHandler(io.Discard, nil)),
		Reboot: func(context.Context) error {
			rebooted.Store(true)
			return nil
		},
	}
	return s, &rebooted
}

// The whole point of the package: reboot is the last rung, and every
// cheaper recovery is tried first.
func TestLadderClimbsInOrderAndRebootsLast(t *testing.T) {
	// Succeeds once so the link counts as having worked, then fails
	// forever — reboot is only reachable for a link that can be recovered
	// *to* something.
	probe := &flakyProbe{succeedFirst: 1}
	runner := newFakeRunner()
	s, rebooted := testSupervisor(probe, runner)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		s.Run(ctx)
	}()

	// Wait for the ladder to be exhausted.
	deadline := time.After(2 * time.Second)
	for !rebooted.Load() {
		select {
		case <-deadline:
			t.Fatalf("never reached reboot; got rung %v after %d commands: %v",
				s.Status().Rung, len(runner.commands()), runner.commands())
		case <-time.After(5 * time.Millisecond):
		}
	}
	cancel()
	<-done

	cmds := runner.commands()

	// Every cheaper rung must have been attempted before the reboot.
	for _, want := range []string{"wpa_cli", "wpa_supplicant", "rmmod", "modprobe"} {
		if !runner.ran(want) {
			t.Errorf("reboot happened without ever running %q; commands were %v", want, cmds)
		}
	}

	// And in the right order: reassociate before restart before reload.
	idx := func(substr string) int {
		for i, c := range cmds {
			if strings.Contains(c, substr) {
				return i
			}
		}
		return -1
	}
	if a, b := idx("wpa_cli"), idx("rmmod"); a < 0 || b < 0 || a > b {
		t.Errorf("reassociate (%d) should precede driver reload (%d)", a, b)
	}
}

// A blip must not restart anything: escalation requires sustained failure.
func TestSingleFailureDoesNotEscalate(t *testing.T) {
	probe := &scriptedProbe{failFirst: 1}
	runner := newFakeRunner()
	s, rebooted := testSupervisor(probe, runner)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	s.Run(ctx)

	if rebooted.Load() {
		t.Error("rebooted after a single probe failure")
	}
	if cmds := runner.commands(); len(cmds) > 0 {
		t.Errorf("ran recovery commands after one failure: %v", cmds)
	}
}

// Recovery must reset the ladder, so a link that comes back is not left one
// failure away from a reboot.
func TestRecoveryResetsLadder(t *testing.T) {
	probe := &scriptedProbe{failFirst: 2} // two failures, then healthy
	runner := newFakeRunner()
	s, rebooted := testSupervisor(probe, runner)

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	s.Run(ctx)

	st := s.Status()
	if !st.Up {
		t.Error("status reports down after the probe recovered")
	}
	if st.Rung != RungNone {
		t.Errorf("Rung = %v after recovery, want RungNone", st.Rung)
	}
	if st.Failures != 0 {
		t.Errorf("Failures = %d after recovery, want 0", st.Failures)
	}
	if rebooted.Load() {
		t.Error("rebooted despite the link recovering")
	}
	// It should have tried the cheapest rung once, and gone no further.
	if !runner.ran("wpa_cli") {
		t.Error("never attempted reassociation")
	}
	if runner.ran("rmmod") {
		t.Error("reloaded the driver for a link that recovered on its own")
	}
}

// A failing recovery command must not stop the ladder — that would strand
// the device at whatever rung threw.
func TestFailingCommandStillEscalates(t *testing.T) {
	probe := &flakyProbe{succeedFirst: 1}
	runner := newFakeRunner()
	runner.fail["wpa_cli"] = errors.New("wpa_cli not found")
	runner.fail["rmmod"] = errors.New("module in use")

	s, rebooted := testSupervisor(probe, runner)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		s.Run(ctx)
	}()

	deadline := time.After(2 * time.Second)
	for !rebooted.Load() {
		select {
		case <-deadline:
			t.Fatalf("stalled at rung %v when recovery commands failed", s.Status().Rung)
		case <-time.After(5 * time.Millisecond):
		}
	}
	cancel()
	<-done
}

func TestStatusIsPublished(t *testing.T) {
	probe := &scriptedProbe{failFirst: 0}
	runner := newFakeRunner()
	s, _ := testSupervisor(probe, runner)

	var mu sync.Mutex
	var seen []Status
	s.OnStatus = func(st Status) {
		mu.Lock()
		defer mu.Unlock()
		seen = append(seen, st)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	s.Run(ctx)

	mu.Lock()
	defer mu.Unlock()
	if len(seen) == 0 {
		t.Fatal("OnStatus was never called")
	}
	if !seen[0].Up {
		t.Error("first published status reports the link down")
	}
}

// Regression test for a boot loop observed on real hardware.
//
// A Pi with no wpa_supplicant.conf climbed the entire ladder and rebooted,
// then did it again roughly every six minutes, forever. Rebooting is a way
// to recover something that was previously working; a link that has never
// come up has nothing to recover to, so the ladder must stop below reboot.
func TestNeverConnectedDoesNotReboot(t *testing.T) {
	probe := alwaysFailProbe{}
	runner := newFakeRunner()
	s, rebooted := testSupervisor(probe, runner)

	ctx, cancel := context.WithTimeout(context.Background(), 700*time.Millisecond)
	defer cancel()
	s.Run(ctx)

	if rebooted.Load() {
		t.Fatal("rebooted a device that has never had a working link")
	}
	// It should still have tried every cheaper recovery.
	for _, want := range []string{"wpa_cli", "wpa_supplicant", "modprobe"} {
		if !runner.ran(want) {
			t.Errorf("never attempted %q before giving up", want)
		}
	}
	if got := s.Status().Rung; got != RungReloadDriver {
		t.Errorf("Rung = %v, want to hold at RungReloadDriver", got)
	}
}

// Once a link has worked, a later failure may legitimately reach reboot.
func TestPreviouslyConnectedCanReboot(t *testing.T) {
	// Succeed once, then fail forever.
	probe := &flakyProbe{succeedFirst: 1}
	runner := newFakeRunner()
	s, rebooted := testSupervisor(probe, runner)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	done := make(chan struct{})
	go func() { defer close(done); s.Run(ctx) }()

	deadline := time.After(2 * time.Second)
	for !rebooted.Load() {
		select {
		case <-deadline:
			t.Fatalf("a link that worked and then died never reached reboot; rung=%v", s.Status().Rung)
		case <-time.After(5 * time.Millisecond):
		}
	}
	cancel()
	<-done
}

// While the setup portal owns the radio there is no route anywhere by
// design, so recovery must not run at all.
func TestSuspendedDoesNothing(t *testing.T) {
	probe := alwaysFailProbe{}
	runner := newFakeRunner()
	s, rebooted := testSupervisor(probe, runner)
	s.Suspended = func() bool { return true }

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	s.Run(ctx)

	if rebooted.Load() {
		t.Error("rebooted while suspended")
	}
	if cmds := runner.commands(); len(cmds) > 0 {
		t.Errorf("ran recovery commands while suspended: %v", cmds)
	}
}

// flakyProbe succeeds for the first N calls, then fails forever.
type flakyProbe struct {
	mu           sync.Mutex
	succeedFirst int
	calls        int
}

func (p *flakyProbe) Probe(context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	if p.calls <= p.succeedFirst {
		return nil
	}
	return errors.New("link lost")
}

// hangingRunner blocks until its context is cancelled, modelling a command
// talking to a radio driver that has stopped answering — which is the state
// the ladder is running because of.
type hangingRunner struct {
	mu     sync.Mutex
	calls  []string
	block  map[string]bool
	served chan string
}

func (h *hangingRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	h.mu.Lock()
	h.calls = append(h.calls, name)
	blocked := h.block[name]
	h.mu.Unlock()

	select {
	case h.served <- name:
	default:
	}

	if blocked {
		<-ctx.Done() // never returns on its own
		return nil, ctx.Err()
	}
	return nil, nil
}

func (h *hangingRunner) ran(name string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, c := range h.calls {
		if c == name {
			return true
		}
	}
	return false
}

// A recovery command that never returns must not take the ladder with it.
//
// Nothing else is watching this goroutine: the hardware watchdog is fed by
// the render loop, which carries on drawing a clock above a dead network. A
// supervisor blocked inside act stops probing, never climbs to the rung that
// would have fixed things, and never reaches reboot — so the mirror keeps
// showing the time and cannot be reached by anyone, forever.
func TestHangingRecoveryCommandDoesNotStallTheLadder(t *testing.T) {
	probe := &flakyProbe{succeedFirst: 1}
	runner := &hangingRunner{
		block:  map[string]bool{"wpa_cli": true},
		served: make(chan string, 16),
	}

	s, rebooted := testSupervisor(probe, runner)
	s.Runner = runner
	s.CommandTimeout = 50 * time.Millisecond

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		s.Run(ctx)
	}()

	deadline := time.After(5 * time.Second)
	for !rebooted.Load() {
		select {
		case <-deadline:
			t.Fatalf("stalled at rung %v; the ladder never got past the hanging command",
				s.Status().Rung)
		case <-time.After(5 * time.Millisecond):
		}
	}
	cancel()
	<-done

	// It must have climbed past the rung that hung.
	if !runner.ran("wpa_cli") {
		t.Error("never attempted the cheapest rung")
	}
	if !runner.ran("modprobe") {
		t.Error("never reached the driver reload rung")
	}
}

// The timeout must not be so eager that a slow but working command is killed.
func TestSlowCommandWithinTheTimeoutIsAllowedToFinish(t *testing.T) {
	runner := &hangingRunner{block: map[string]bool{}, served: make(chan string, 16)}
	s, _ := testSupervisor(&scriptedProbe{failFirst: 2}, runner)
	s.Runner = runner
	s.CommandTimeout = time.Second

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	s.Run(ctx)

	if !runner.ran("wpa_cli") {
		t.Error("the first recovery command never ran")
	}
	if st := s.Status(); st.Rung > RungReassociate {
		t.Errorf("climbed to %v despite the command succeeding", st.Rung)
	}
}
