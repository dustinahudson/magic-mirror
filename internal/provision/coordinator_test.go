package provision

import (
	"context"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"
)

// recorder captures the commands a Portal would have run.
type recorder struct {
	mu   sync.Mutex
	cmds []string
}

func (r *recorder) Run(_ context.Context, name string, args ...string) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cmds = append(r.cmds, strings.TrimSpace(name+" "+strings.Join(args, " ")))
	return nil, nil
}

func (r *recorder) ran(prefix string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, c := range r.cmds {
		if strings.HasPrefix(c, prefix) {
			return true
		}
	}
	return false
}

func testCoordinator(t *testing.T) (*Coordinator, *recorder) {
	t.Helper()
	p := testPortal(t)
	rec := &recorder{}
	p.Runner = rec
	return NewCoordinator(p, slog.New(slog.NewTextHandler(io.Discard, nil))), rec
}

func TestForgetErasesCredentialsAndDropsTheLink(t *testing.T) {
	c, rec := testCoordinator(t)

	if err := os.WriteFile(c.Portal.WPAConfPath,
		[]byte("network={\n\tssid=\"Home\"\n}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := c.Forget(context.Background()); err != nil {
		t.Fatalf("Forget: %v", err)
	}

	if _, err := os.Stat(c.Portal.WPAConfPath); !os.IsNotExist(err) {
		t.Errorf("credentials still on disk: %v", err)
	}

	// Erasing the file is only half of it. Without dropping the link the
	// supplicant keeps the association it already has, the coordinator goes on
	// seeing a connected mirror, and the portal never comes up — which looks
	// exactly like the button having done nothing.
	if !rec.ran("killall wpa_supplicant") {
		t.Errorf("supplicant left running; commands were %v", rec.cmds)
	}
	if !rec.ran("ip addr flush dev wlan0") {
		t.Errorf("address not flushed; commands were %v", rec.cmds)
	}
}

// Forgetting a network that was never saved is not a failure. Someone pressing
// the button twice, or pressing it on a mirror provisioned by hand, wants the
// same end state either way.
func TestForgetWithNoCredentialsSucceeds(t *testing.T) {
	c, _ := testCoordinator(t)

	if err := c.Forget(context.Background()); err != nil {
		t.Fatalf("Forget with no saved network: %v", err)
	}
}

func TestForgetRefusesWithoutACredentialsPath(t *testing.T) {
	c, rec := testCoordinator(t)
	c.Portal.WPAConfPath = ""

	if err := c.Forget(context.Background()); err == nil {
		t.Fatal("Forget succeeded with no credentials file configured")
	}
	// It must also not have taken the link down. Dropping the network on a
	// mirror it cannot then re-provision is the one outcome worse than
	// refusing.
	if len(rec.cmds) != 0 {
		t.Errorf("ran commands despite refusing: %v", rec.cmds)
	}
}

// Run treats "no credentials on disk" as grounds to start the portal without
// waiting out the grace period. Forget relies on that — it does no starting of
// its own — so the two must agree on what "no credentials" looks like.
func TestForgetIsVisibleToTheRunLoop(t *testing.T) {
	c, _ := testCoordinator(t)

	if err := os.WriteFile(c.Portal.WPAConfPath, []byte("network={}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !fileExists(c.Portal.WPAConfPath) {
		t.Fatal("saved credentials not seen by the run loop's check")
	}

	if err := c.Forget(context.Background()); err != nil {
		t.Fatal(err)
	}
	if fileExists(c.Portal.WPAConfPath) {
		t.Error("run loop would still see credentials after Forget")
	}
}
