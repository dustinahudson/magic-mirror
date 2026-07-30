// Package provision runs the WiFi setup portal.
//
// When the mirror cannot join a network — first boot, a changed password, a
// house move — it becomes an access point and serves a page where you pick a
// network from a phone. That removes the failure this appliance is otherwise
// most likely to hit: needing a card reader and a laptop to change one
// string.
//
// The portal only exists while the mirror is unable to connect. It tears
// itself down as soon as credentials are accepted and association succeeds.
package provision

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Defaults for the setup network.
const (
	DefaultSSID = "MagicMirror-Setup"
	DefaultIP   = "192.168.4.1"
	DefaultCIDR = "192.168.4.1/24"
	dhcpStart   = "192.168.4.10"
	dhcpEnd     = "192.168.4.50"
)

// Network is a scanned access point.
type Network struct {
	SSID   string `json:"ssid"`
	Signal int    `json:"signal"` // dBm
	Secure bool   `json:"secure"`
}

// Runner executes commands. Injected for testing.
type Runner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

// ExecRunner runs real commands.
type ExecRunner struct{}

func (ExecRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}

// Portal manages the AP lifecycle.
type Portal struct {
	// Interface is the wireless interface, e.g. wlan0.
	Interface string

	// SSID of the setup network.
	SSID string

	// WPAConfPath is where accepted credentials are written — the durable
	// copy on the FAT partition.
	WPAConfPath string

	// RunDir holds generated hostapd and dnsmasq configs.
	RunDir string

	Runner Runner
	Log    *slog.Logger

	mu      sync.Mutex
	running bool
}

// New returns a Portal with workable defaults.
func New(iface, wpaConfPath string, log *slog.Logger) *Portal {
	return &Portal{
		Interface:   iface,
		SSID:        DefaultSSID,
		WPAConfPath: wpaConfPath,
		RunDir:      "/var/run/magicmirror",
		Runner:      ExecRunner{},
		Log:         log,
	}
}

// Running reports whether the AP is currently up.
func (p *Portal) Running() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.running
}

// Scan lists visible networks.
//
// Run before starting the AP: once hostapd owns the radio, scanning is no
// longer possible, so the list is captured up front and reused by the portal.
func (p *Portal) Scan(ctx context.Context) ([]Network, error) {
	out, err := p.Runner.Run(ctx, "iw", "dev", p.Interface, "scan")
	if err != nil {
		return nil, fmt.Errorf("scan: %w", err)
	}
	return parseScan(string(out)), nil
}

// parseScan reads `iw scan` output.
func parseScan(out string) []Network {
	byySSID := map[string]Network{}
	var cur Network
	var haveSSID bool

	flush := func() {
		if !haveSSID || cur.SSID == "" {
			return
		}
		// Keep the strongest sighting of each SSID: a mesh network appears
		// once per AP, and listing it five times helps nobody.
		if prev, ok := byySSID[cur.SSID]; !ok || cur.Signal > prev.Signal {
			byySSID[cur.SSID] = cur
		}
	}

	for _, line := range strings.Split(out, "\n") {
		t := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(t, "BSS "):
			flush()
			cur, haveSSID = Network{Signal: -100}, false
		case strings.HasPrefix(t, "SSID: "):
			cur.SSID = strings.TrimPrefix(t, "SSID: ")
			haveSSID = true
		case strings.HasPrefix(t, "signal: "):
			f := strings.Fields(strings.TrimPrefix(t, "signal: "))
			if len(f) > 0 {
				if v, err := strconv.ParseFloat(f[0], 64); err == nil {
					cur.Signal = int(v)
				}
			}
		case strings.Contains(t, "RSN:") || strings.Contains(t, "WPA:"),
			strings.Contains(t, "Privacy"):
			cur.Secure = true
		}
	}
	flush()

	list := make([]Network, 0, len(byySSID))
	for _, n := range byySSID {
		list = append(list, n)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Signal > list[j].Signal })
	return list
}

// Start brings up the access point.
func (p *Portal) Start(ctx context.Context) error {
	p.mu.Lock()
	if p.running {
		p.mu.Unlock()
		return nil
	}
	p.mu.Unlock()

	if err := os.MkdirAll(p.RunDir, 0o755); err != nil {
		return fmt.Errorf("run dir: %w", err)
	}

	hostapdConf := filepath.Join(p.RunDir, "hostapd.conf")
	dnsmasqConf := filepath.Join(p.RunDir, "dnsmasq.conf")

	// Open network on purpose. This is a short-lived setup flow reached from
	// a phone, and a WPA2 password would have to be printed somewhere — on a
	// DIY build there is no sticker to print it on. See QUESTIONS.md 4.2.
	if err := os.WriteFile(hostapdConf, []byte(fmt.Sprintf(
		"interface=%s\ndriver=nl80211\nssid=%s\nhw_mode=g\nchannel=6\n"+
			"auth_algs=1\nwmm_enabled=0\nignore_broadcast_ssid=0\n",
		p.Interface, p.SSID)), 0o644); err != nil {
		return fmt.Errorf("write hostapd.conf: %w", err)
	}

	if err := os.WriteFile(dnsmasqConf, []byte(fmt.Sprintf(
		"interface=%s\nbind-interfaces\nexcept-interface=lo\n"+
			"dhcp-range=%s,%s,12h\n"+
			"dhcp-option=3,%s\ndhcp-option=6,%s\n"+
			// Resolve every name to the portal, so a phone's connectivity
			// check fails in the way that makes it pop the login sheet.
			"address=/#/%s\n",
		p.Interface, dhcpStart, dhcpEnd, DefaultIP, DefaultIP, DefaultIP)), 0o644); err != nil {
		return fmt.Errorf("write dnsmasq.conf: %w", err)
	}

	// The supplicant must release the radio before hostapd can claim it.
	_, _ = p.Runner.Run(ctx, "killall", "wpa_supplicant")
	_, _ = p.Runner.Run(ctx, "killall", "udhcpc")
	time.Sleep(500 * time.Millisecond)

	if _, err := p.Runner.Run(ctx, "ip", "addr", "flush", "dev", p.Interface); err != nil {
		p.Log.Warn("could not flush interface address", "err", err)
	}
	if _, err := p.Runner.Run(ctx, "ip", "addr", "add", DefaultCIDR, "dev", p.Interface); err != nil {
		return fmt.Errorf("assign %s: %w", DefaultCIDR, err)
	}
	if _, err := p.Runner.Run(ctx, "ip", "link", "set", p.Interface, "up"); err != nil {
		return fmt.Errorf("bring up %s: %w", p.Interface, err)
	}

	if _, err := p.Runner.Run(ctx, "hostapd", "-B", hostapdConf); err != nil {
		return fmt.Errorf("hostapd: %w", err)
	}
	if _, err := p.Runner.Run(ctx, "dnsmasq", "-C", dnsmasqConf); err != nil {
		// An AP with no DHCP is useless, so unwind rather than leave a
		// half-started portal that looks joinable and is not.
		_, _ = p.Runner.Run(ctx, "killall", "hostapd")
		return fmt.Errorf("dnsmasq: %w", err)
	}

	p.mu.Lock()
	p.running = true
	p.mu.Unlock()

	p.Log.Info("setup portal started", "ssid", p.SSID, "url", "http://"+DefaultIP)
	return nil
}

// Stop tears the access point down and restores client mode.
func (p *Portal) Stop(ctx context.Context) error {
	p.mu.Lock()
	if !p.running {
		p.mu.Unlock()
		return nil
	}
	p.running = false
	p.mu.Unlock()

	_, _ = p.Runner.Run(ctx, "killall", "dnsmasq")
	_, _ = p.Runner.Run(ctx, "killall", "hostapd")
	_, _ = p.Runner.Run(ctx, "ip", "addr", "flush", "dev", p.Interface)

	p.Log.Info("setup portal stopped")
	return nil
}

// SaveCredentials writes a wpa_supplicant.conf for the chosen network and
// restarts client mode.
//
// Written atomically: a power cut mid-save must not leave a truncated
// supplicant config, which would be a device that can never rejoin.
func (p *Portal) SaveCredentials(ctx context.Context, ssid, psk, country string) error {
	if strings.TrimSpace(ssid) == "" {
		return fmt.Errorf("network name is required")
	}
	if country == "" {
		country = "US"
	}

	var b strings.Builder
	fmt.Fprintf(&b, "country=%s\n", country)
	b.WriteString("ctrl_interface=/var/run/wpa_supplicant\n")
	b.WriteString("update_config=1\n\n")
	b.WriteString("network={\n")
	fmt.Fprintf(&b, "\tssid=%q\n", ssid)
	if psk == "" {
		b.WriteString("\tkey_mgmt=NONE\n")
	} else {
		if len(psk) < 8 || len(psk) > 63 {
			return fmt.Errorf("password must be between 8 and 63 characters")
		}
		fmt.Fprintf(&b, "\tpsk=%q\n", psk)
		b.WriteString("\tkey_mgmt=WPA-PSK\n")
	}
	b.WriteString("}\n")

	dir := filepath.Dir(p.WPAConfPath)
	tmp, err := os.CreateTemp(dir, ".wpa-*")
	if err != nil {
		return fmt.Errorf("create temp supplicant config: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if _, err := tmp.WriteString(b.String()); err != nil {
		tmp.Close()
		return fmt.Errorf("write supplicant config: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("sync supplicant config: %w", err)
	}
	tmp.Close()

	if err := os.Chmod(tmpName, 0o600); err != nil {
		return fmt.Errorf("chmod supplicant config: %w", err)
	}
	if err := os.Rename(tmpName, p.WPAConfPath); err != nil {
		return fmt.Errorf("install supplicant config: %w", err)
	}

	p.Log.Info("wifi credentials saved", "ssid", ssid, "secured", psk != "")
	return nil
}

// Reconnect stops the AP and brings the client link back up.
func (p *Portal) Reconnect(ctx context.Context) error {
	if err := p.Stop(ctx); err != nil {
		return err
	}
	if _, err := p.Runner.Run(ctx, "/etc/init.d/S40network", "restart"); err != nil {
		return fmt.Errorf("restart network: %w", err)
	}
	return nil
}
