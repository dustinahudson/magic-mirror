package netmon

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
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

func testSupervisor(probe Prober, runner Runner) (*Supervisor, *bool) {
	rebooted := false
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
			rebooted = true
			return nil
		},
	}
	return s, &rebooted
}

// The whole point of the package: reboot is the last rung, and every
// cheaper recovery is tried first.
func TestLadderClimbsInOrderAndRebootsLast(t *testing.T) {
	probe := &scriptedProbe{failFirst: 1000} // never recovers
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
	for !*rebooted {
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

	if *rebooted {
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
	if *rebooted {
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
	probe := &scriptedProbe{failFirst: 1000}
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
	for !*rebooted {
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
