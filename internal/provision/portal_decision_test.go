package provision

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"
)

// The setup portal is the difference between "reconfigure it from a phone"
// and "drive over, take it off the wall, bring the card home". Every decision
// tested here is one where getting it wrong means the second thing.

// fakeLink reports whatever the test says, and counts how often it was asked.
type fakeLink struct {
	mu sync.Mutex
	up bool
}

func (f *fakeLink) HasLink(string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.up
}

func (f *fakeLink) set(up bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.up = up
}

// waitForMode polls until the coordinator reaches a mode, or gives up.
func waitForMode(t *testing.T, c *Coordinator, want Mode, within time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if c.State().Mode == want {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return false
}

// With no credentials there is nothing to wait for. Sitting in the grace
// period would leave a fresh mirror dark and unreachable for no reason.
func TestPortalStartsImmediatelyWithNoCredentials(t *testing.T) {
	c, rec := testCoordinator(t)
	link := &fakeLink{up: false}
	c.Link = link
	c.Poll = 5 * time.Millisecond
	c.Grace = time.Hour // must be bypassed entirely
	if err := os.Remove(c.Portal.WPAConfPath); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.Run(ctx)

	if !waitForMode(t, c, ModePortal, 2*time.Second) {
		t.Fatalf("portal never started without credentials; mode = %v", c.State().Mode)
	}
	if !rec.ran("ip") && !rec.ran("hostapd") {
		t.Error("portal mode was reported but no interface commands ran")
	}
}

// With credentials, a blip must not throw the mirror into setup mode. An
// appliance that drops its display every time the router reboots is worse
// than one that waits.
func TestPortalWaitsOutTheGraceWhenCredentialsExist(t *testing.T) {
	c, _ := testCoordinator(t)
	link := &fakeLink{up: false}
	c.Link = link
	c.Poll = 5 * time.Millisecond
	c.Grace = time.Hour

	if err := os.WriteFile(c.Portal.WPAConfPath, []byte("network={}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.Run(ctx)

	if !waitForMode(t, c, ModeConnecting, time.Second) {
		t.Fatalf("expected to be connecting, got %v", c.State().Mode)
	}
	time.Sleep(100 * time.Millisecond)
	if got := c.State().Mode; got == ModePortal {
		t.Error("started the setup portal during the grace period")
	}
}

// But it must give up eventually. A wrong password saved months ago is not a
// blip, and waiting forever is how a mirror becomes unreachable.
func TestPortalStartsOnceTheGraceExpires(t *testing.T) {
	c, _ := testCoordinator(t)
	c.Link = &fakeLink{up: false}
	c.Poll = 5 * time.Millisecond
	c.Grace = 20 * time.Millisecond

	if err := os.WriteFile(c.Portal.WPAConfPath, []byte("network={}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.Run(ctx)

	if !waitForMode(t, c, ModePortal, 2*time.Second) {
		t.Fatalf("portal never started after the grace expired; mode = %v", c.State().Mode)
	}
}

// A working network must never be interrupted, however long it has been up.
func TestNoPortalWhileTheLinkIsUp(t *testing.T) {
	c, rec := testCoordinator(t)
	c.Link = &fakeLink{up: true}
	c.Poll = 5 * time.Millisecond
	c.Grace = 10 * time.Millisecond

	// No credentials file at all — still must not start the portal, because
	// the link is demonstrably working.
	if err := os.Remove(c.Portal.WPAConfPath); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.Run(ctx)

	if !waitForMode(t, c, ModeConnected, time.Second) {
		t.Fatalf("never reported connected; mode = %v", c.State().Mode)
	}
	time.Sleep(100 * time.Millisecond)

	if c.State().Mode != ModeConnected {
		t.Errorf("left connected state while the link was up: %v", c.State().Mode)
	}
	if rec.ran("hostapd") {
		t.Error("started an access point while the network was working")
	}
}

// The grace clock restarts whenever the link returns, so an hour of healthy
// uptime is not carried forward into the next outage as "already waited".
func TestRecoveringLinkResetsTheGrace(t *testing.T) {
	c, _ := testCoordinator(t)
	link := &fakeLink{up: true}
	c.Link = link
	c.Poll = 5 * time.Millisecond
	c.Grace = 200 * time.Millisecond

	if err := os.WriteFile(c.Portal.WPAConfPath, []byte("network={}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.Run(ctx)

	if !waitForMode(t, c, ModeConnected, time.Second) {
		t.Fatal("never connected")
	}

	// Drop the link briefly, then restore it well inside the grace window.
	link.set(false)
	time.Sleep(50 * time.Millisecond)
	link.set(true)

	time.Sleep(100 * time.Millisecond)
	if got := c.State().Mode; got == ModePortal {
		t.Error("a brief outage started the setup portal")
	}
}

// State changes have to reach the screen. A mirror that is in setup mode and
// does not say so is indistinguishable from broken hardware, which is exactly
// when someone gets in a car.
func TestStateChangesArePublished(t *testing.T) {
	c, _ := testCoordinator(t)
	c.Link = &fakeLink{up: true}
	c.Poll = 5 * time.Millisecond

	var mu sync.Mutex
	var seen []Mode
	c.OnState = func(s State) {
		mu.Lock()
		defer mu.Unlock()
		seen = append(seen, s.Mode)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.Run(ctx)

	if !waitForMode(t, c, ModeConnected, time.Second) {
		t.Fatal("never connected")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(seen) == 0 {
		t.Fatal("no state was ever published")
	}
	if seen[0] != ModeConnecting {
		t.Errorf("first published state = %v, want %v", seen[0], ModeConnecting)
	}
}

// Shutdown must tear the access point down. Leaving an open network called
// MagicMirror-Setup broadcasting in somebody's house is not acceptable.
func TestShutdownStopsThePortal(t *testing.T) {
	c, rec := testCoordinator(t)
	c.Link = &fakeLink{up: false}
	c.Poll = 5 * time.Millisecond
	c.Grace = 10 * time.Millisecond
	if err := os.Remove(c.Portal.WPAConfPath); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	go c.Run(ctx)

	if !waitForMode(t, c, ModePortal, 2*time.Second) {
		t.Fatal("portal never started")
	}
	cancel()

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if rec.ran("killall") || rec.ran("ip link set") {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Error("no teardown commands ran after shutdown")
}
