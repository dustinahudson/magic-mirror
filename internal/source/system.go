package source

import (
	"context"
	"net"
	"os"
	"time"
)

// KeySystem is the store key system information is published under.
const KeySystem = "system"

// SystemInfo is what the mirror knows about itself.
type SystemInfo struct {
	Hostname  string
	IP        string
	Version   string
	StartedAt time.Time

	// Link reports whether an interface with a routable address exists.
	// The wifi supervisor publishes richer state separately; this is just
	// "do we appear to be on a network".
	Link bool
}

// Uptime is how long the process has been running.
func (s SystemInfo) Uptime(now time.Time) time.Duration {
	if s.StartedAt.IsZero() {
		return 0
	}
	return now.Sub(s.StartedAt)
}

// SystemSource publishes local host information.
//
// Cheap and local, so it refreshes often — the IP appearing on screen is how
// you find the config page after a fresh boot, and waiting minutes for it
// would be its own small usability failure.
type SystemSource struct {
	Version   string
	StartedAt time.Time
	Interval_ time.Duration
}

// NewSystem returns a system information fetcher.
func NewSystem(version string, interval time.Duration) *SystemSource {
	if interval <= 0 {
		interval = 15 * time.Second
	}
	return &SystemSource{
		Version:   version,
		StartedAt: time.Now(),
		Interval_: interval,
	}
}

func (s *SystemSource) Key() string             { return KeySystem }
func (s *SystemSource) Interval() time.Duration { return s.Interval_ }
func (s *SystemSource) Timeout() time.Duration  { return 5 * time.Second }

func (s *SystemSource) Fetch(context.Context) (any, error) {
	host, _ := os.Hostname()
	ip, link := primaryIP()
	return SystemInfo{
		Hostname:  host,
		IP:        ip,
		Version:   s.Version,
		StartedAt: s.StartedAt,
		Link:      link,
	}, nil
}

// primaryIP returns the first non-loopback IPv4 address on an up interface.
func primaryIP() (string, bool) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return "", false
	}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			ipnet, ok := addr.(*net.IPNet)
			if !ok {
				continue
			}
			if v4 := ipnet.IP.To4(); v4 != nil && !v4.IsLoopback() && !v4.IsLinkLocalUnicast() {
				return v4.String(), true
			}
		}
	}
	return "", false
}
