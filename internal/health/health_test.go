package health

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// Everything in this file is about one question: does the device come back on
// its own, or does somebody have to drive over and pull the SD card?
//
// mm-supervise counts every start as a failure and reverts to the previous
// binary after three that never reached healthy. So marking too early keeps a
// crashing build installed forever, and never marking reverts a build that
// works. Both end the same way — a mirror on a wall in another house, and a
// card reader on a desk in this one.

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", filepath.Base(path), err)
	}
	return string(b)
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// A build must not call itself good before it has survived long enough to
// prove it. Marking on the first frame would bless a binary that dies as soon
// as it touches the framebuffer.
func TestDoesNotMarkHealthyBeforeTheGracePeriod(t *testing.T) {
	dir := t.TempDir()
	m := New(Options{StateDir: dir, HealthyAfter: time.Hour}, quiet())

	for range 5 {
		m.Pet()
	}

	if m.Healthy() {
		t.Error("declared healthy immediately")
	}
	if exists(filepath.Join(dir, "health")) {
		t.Error("wrote a health marker before the grace period elapsed")
	}
}

// And it must call itself good once it has, because that is the only thing
// that stops an automatic revert of a working build.
func TestMarksHealthyAfterTheGracePeriod(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "failures"), []byte("2\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	m := New(Options{StateDir: dir, HealthyAfter: 10 * time.Millisecond}, quiet())
	time.Sleep(20 * time.Millisecond)
	m.Pet()

	if !m.Healthy() {
		t.Fatal("still not healthy after the grace period")
	}

	marker := read(t, filepath.Join(dir, "health"))
	if !strings.HasPrefix(marker, "healthy at ") {
		t.Errorf("health marker = %q, want a 'healthy at ...' line", marker)
	}

	// The counter is what mm-supervise reads to decide on a rollback.
	if got := read(t, filepath.Join(dir, "failures")); got != "0\n" {
		t.Errorf("failures = %q, want %q — a revert is still armed", got, "0\n")
	}
}

// Marking is a one-time transition. Rewriting the marker on every frame would
// be thousands of needless writes to a FAT card that already loses files.
func TestMarksHealthyOnlyOnce(t *testing.T) {
	dir := t.TempDir()
	m := New(Options{StateDir: dir, HealthyAfter: 10 * time.Millisecond}, quiet())
	time.Sleep(20 * time.Millisecond)

	m.Pet()
	first := read(t, filepath.Join(dir, "health"))

	if err := os.Remove(filepath.Join(dir, "health")); err != nil {
		t.Fatal(err)
	}
	for range 10 {
		m.Pet()
	}

	if exists(filepath.Join(dir, "health")) {
		t.Errorf("health marker rewritten after the first mark (was %q)", first)
	}
}

// The clock file is what makes HTTPS work before NTP. Without it the device
// boots at 1970 and every certificate is "not yet valid", so the calendar and
// weather fail for reasons no one can see from the outside.
func TestPersistsTheClock(t *testing.T) {
	dir := t.TempDir()
	m := New(Options{StateDir: dir, HealthyAfter: time.Hour}, quiet())

	m.Pet()

	got := strings.TrimSpace(read(t, filepath.Join(dir, "clock")))
	// BusyBox date's native format, which is what S15clock parses back.
	if !regexp.MustCompile(`^\d{12}\.\d{2}$`).MatchString(got) {
		t.Errorf("clock = %q, want YYYYMMDDhhmm.ss", got)
	}
}

// Once every five minutes, not once every frame. At one frame a second the
// difference is three hundred writes a minute to a card that is already the
// least reliable part of this device.
func TestThrottlesClockWrites(t *testing.T) {
	dir := t.TempDir()
	m := New(Options{StateDir: dir, HealthyAfter: time.Hour}, quiet())

	m.Pet()
	if err := os.Remove(filepath.Join(dir, "clock")); err != nil {
		t.Fatal(err)
	}
	for range 20 {
		m.Pet()
	}

	if exists(filepath.Join(dir, "clock")) {
		t.Error("clock rewritten within the throttle interval")
	}
}

// A laptop has no watchdog and no state directory, and the mirror has to run
// there too — that is the loop that avoids needing the card at all.
func TestRunsWithoutAStateDirectory(t *testing.T) {
	m := New(Options{HealthyAfter: 10 * time.Millisecond}, quiet())
	time.Sleep(20 * time.Millisecond)

	m.Pet() // must not panic
	if !m.Healthy() {
		t.Error("a monitor with no state directory never becomes healthy")
	}
	if err := m.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
}

// A missing watchdog device is tolerated by design: it is a safety net, not a
// requirement. Failing here would stop the mirror booting on any machine
// without /dev/watchdog.
func TestMissingWatchdogIsTolerated(t *testing.T) {
	dir := t.TempDir()
	m := New(Options{
		StateDir:     dir,
		WatchdogPath: filepath.Join(dir, "no-such-watchdog"),
		HealthyAfter: 10 * time.Millisecond,
	}, quiet())

	time.Sleep(20 * time.Millisecond)
	m.Pet() // must not panic

	if !m.Healthy() {
		t.Error("a missing watchdog stopped the monitor working")
	}
	if err := m.Close(); err != nil {
		t.Errorf("Close with no watchdog: %v", err)
	}
}

// An unwritable state directory must degrade, not crash. A panic here is a
// crash loop, and a crash loop is a car journey.
func TestUnwritableStateDirDoesNotPanic(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o755) })

	m := New(Options{StateDir: dir, HealthyAfter: 10 * time.Millisecond}, quiet())
	time.Sleep(20 * time.Millisecond)

	m.Pet() // must not panic

	// It still considers itself healthy in memory; it simply could not say so
	// on disk. Reporting otherwise would be a lie the render loop acts on.
	if !m.Healthy() {
		t.Error("an unwritable state dir made the process report itself unhealthy")
	}
}

// Defaults have to be sane, because every one of them is a decision about how
// long a broken device stays broken.
func TestDefaultsAreApplied(t *testing.T) {
	m := New(Options{}, quiet())
	if m.healthyAfter != 60*time.Second {
		t.Errorf("healthyAfter = %v, want 60s", m.healthyAfter)
	}
	if m.Healthy() {
		t.Error("a brand new monitor reports itself healthy")
	}
}
