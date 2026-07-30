package provision

import (
	"context"
	"log/slog"
	"net"
	"os"
	"sync"
	"time"
)

// KeyProvision is the store key setup state is published under.
const KeyProvision = "provision"

// Mode is what the provisioning system is currently doing.
type Mode int

const (
	// ModeConnecting means we are waiting for the normal wifi path to
	// associate. The grace period has not expired yet.
	ModeConnecting Mode = iota

	// ModeConnected means the interface has a routable address and the
	// portal is not needed.
	ModeConnected

	// ModePortal means the mirror is acting as an access point and serving
	// the setup page.
	ModePortal

	// ModeFailed means the mirror has no network AND could not bring the
	// setup portal up either.
	//
	// This state exists because the first version had no way to express it:
	// when the portal failed to start, the coordinator silently stayed in
	// ModeConnecting, the overlay stayed inactive, and the display showed a
	// normal mirror with no data and no explanation. A device that cannot
	// help itself must at least say so.
	ModeFailed
)

func (m Mode) String() string {
	switch m {
	case ModeConnected:
		return "connected"
	case ModePortal:
		return "portal"
	case ModeFailed:
		return "failed"
	default:
		return "connecting"
	}
}

// State is what the display and the web UI are told about provisioning.
type State struct {
	Mode Mode
	SSID string // the setup network's name, while in portal mode
	URL  string // where to reach the portal
	IP   string // the mirror's address, once connected

	// Networks is how many access points the last scan found. Shown on
	// screen because "0 networks" and "12 networks" are very different
	// problems and the difference is invisible otherwise.
	Networks int

	// Err explains a ModeFailed state, and is shown on screen.
	Err string

	// Iface is the interface being used, included so the failure screen can
	// say whether it exists at all.
	Iface string

	Since time.Time
}

// LinkChecker reports whether the interface has a usable address.
//
// Deliberately not an internet check. In portal mode there is no internet by
// definition, and on a normal boot the question that matters is "did we
// associate and get an address", which is answerable locally and instantly.
// End-to-end reachability is netmon's job, not this one's.
type LinkChecker interface {
	HasLink(iface string) bool
}

// InterfaceLink checks for a non-loopback, non-link-local IPv4 address.
type InterfaceLink struct{}

func (InterfaceLink) HasLink(iface string) bool {
	ifi, err := net.InterfaceByName(iface)
	if err != nil {
		return false
	}
	if ifi.Flags&net.FlagUp == 0 {
		return false
	}
	addrs, err := ifi.Addrs()
	if err != nil {
		return false
	}
	for _, addr := range addrs {
		ipnet, ok := addr.(*net.IPNet)
		if !ok {
			continue
		}
		v4 := ipnet.IP.To4()
		if v4 == nil || v4.IsLoopback() || v4.IsLinkLocalUnicast() {
			continue
		}
		// The portal assigns itself 192.168.4.1, so seeing that address is
		// not evidence of having joined anything.
		if v4.String() == DefaultIP {
			continue
		}
		return true
	}
	return false
}

// Coordinator decides when the setup portal is needed.
//
// This is the piece that turns "we have an AP implementation" into "the
// mirror is recoverable without a card reader". The rule is deliberately
// conservative: the portal only appears when the normal path has genuinely
// failed, because an appliance that drops into setup mode over a transient
// blip is worse than one that waits.
type Coordinator struct {
	Portal *Portal
	Link   LinkChecker

	// Grace is how long to let the normal wifi path try before giving up
	// and starting the portal. Generous: association plus DHCP on a cold
	// boot can legitimately take twenty seconds.
	Grace time.Duration

	// PortalAddr is where the setup page listens.
	PortalAddr string

	// Poll is how often the link is checked.
	Poll time.Duration

	Log *slog.Logger

	// OnState publishes changes. Never called from the render goroutine.
	OnState func(State)

	mu    sync.Mutex
	state State
}

// NewCoordinator returns a Coordinator with workable defaults.
func NewCoordinator(p *Portal, log *slog.Logger) *Coordinator {
	return &Coordinator{
		Portal:     p,
		Link:       InterfaceLink{},
		Grace:      45 * time.Second,
		PortalAddr: DefaultIP + ":80",
		Poll:       5 * time.Second,
		Log:        log,
	}
}

// State returns the current provisioning state.
func (c *Coordinator) State() State {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.state
}

func (c *Coordinator) setState(s State) {
	s.Since = time.Now()
	c.mu.Lock()
	c.state = s
	c.mu.Unlock()
	if c.OnState != nil {
		c.OnState(s)
	}
}

// Run supervises provisioning until ctx is cancelled.
func (c *Coordinator) Run(ctx context.Context) {
	if c.Poll <= 0 {
		c.Poll = 5 * time.Second
	}
	if c.Grace <= 0 {
		c.Grace = 45 * time.Second
	}

	c.setState(State{Mode: ModeConnecting})
	waitingSince := time.Now()

	for {
		select {
		case <-ctx.Done():
			_ = c.Portal.Stop(context.Background())
			return
		case <-time.After(c.Poll):
		}

		if c.Link.HasLink(c.Portal.Interface) {
			if c.state.Mode != ModeConnected {
				c.Log.Info("network is up; setup portal not needed")
			}
			c.setState(State{Mode: ModeConnected, IP: ipOf(c.Portal.Interface)})
			waitingSince = time.Now()
			continue
		}

		// No link. Decide whether that is worth acting on yet.
		noCreds := !fileExists(c.Portal.WPAConfPath)
		expired := time.Since(waitingSince) >= c.Grace

		if !noCreds && !expired {
			c.setState(State{Mode: ModeConnecting})
			continue
		}

		reason := "no network after grace period"
		if noCreds {
			reason = "no wifi credentials configured"
		}
		c.Log.Warn("starting setup portal", "reason", reason)

		if err := c.runPortal(ctx); err != nil {
			c.Log.Error("setup portal failed", "err", err)
			// Say so on screen. Silently retrying leaves a mirror that
			// shows no data and offers no explanation, which is
			// indistinguishable from broken hardware.
			c.setState(State{Mode: ModeFailed, Err: err.Error()})

			// Back off before retrying so a broken radio does not spin.
			select {
			case <-ctx.Done():
				return
			case <-time.After(30 * time.Second):
			}
		}
		waitingSince = time.Now()
	}
}

// runPortal brings up the AP, serves the setup page, and tears it down once
// credentials are accepted.
func (c *Coordinator) runPortal(ctx context.Context) error {
	// Scan before hostapd claims the radio — once it is an access point,
	// scanning is no longer possible.
	networks, err := c.Portal.Scan(ctx)
	if err != nil {
		c.Log.Warn("scan failed; portal will offer a manual entry only", "err", err)
	}
	c.Log.Info("scanned for networks", "found", len(networks))

	if err := c.Portal.Start(ctx); err != nil {
		return err
	}
	defer func() { _ = c.Portal.Stop(context.Background()) }()

	srv, err := Serve(c.PortalAddr, c.Portal, networks)
	if err != nil {
		return err
	}
	defer srv.Close()

	c.setState(State{
		Mode:     ModePortal,
		SSID:     c.Portal.SSID,
		URL:      "http://" + DefaultIP,
		Networks: len(networks),
	})

	c.Log.Info("setup portal ready",
		"ssid", c.Portal.SSID, "url", "http://"+DefaultIP, "networks", len(networks))

	select {
	case <-ctx.Done():
		return nil
	case <-srv.Done():
	}

	c.Log.Info("credentials accepted; returning to client mode")
	c.setState(State{Mode: ModeConnecting})
	return c.Portal.Reconnect(ctx)
}

func fileExists(path string) bool {
	if path == "" {
		return false
	}
	st, err := os.Stat(path)
	return err == nil && st.Size() > 0
}

func ipOf(iface string) string {
	ifi, err := net.InterfaceByName(iface)
	if err != nil {
		return ""
	}
	addrs, err := ifi.Addrs()
	if err != nil {
		return ""
	}
	for _, addr := range addrs {
		if ipnet, ok := addr.(*net.IPNet); ok {
			if v4 := ipnet.IP.To4(); v4 != nil && !v4.IsLoopback() {
				return v4.String()
			}
		}
	}
	return ""
}
