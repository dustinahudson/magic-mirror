package main

import (
	"strings"
	"testing"

	"github.com/dustinahudson/magic-mirror/internal/config"
)

// A mirror in someone else's house has exactly two remote lifelines: a web UI
// that explains what is wrong, and an updater that can install the fix. Every
// decision here is about whether those get built. Losing either turns a
// software problem into a drive.

func serving(t *testing.T) cliOptions {
	t.Helper()
	return cliOptions{
		ConfigPath: "/boot/config.json",
		FBPath:     "/dev/fb0",
		StateDir:   "/boot",
		WiFiIface:  "wlan0",
		WPAConf:    "/boot/wpa_supplicant.conf",
		Watchdog:   "/dev/watchdog",
	}
}

func TestNoDisplayIsAnError(t *testing.T) {
	_, err := planFrom(cliOptions{}, config.Default())
	if err == nil {
		t.Fatal("planned a mirror with nothing to draw on")
	}
	if !strings.Contains(err.Error(), "-fb") {
		t.Errorf("error %q does not say how to fix it", err)
	}
}

func TestDisplayBackendsCanBeCombined(t *testing.T) {
	o := cliOptions{FBPath: "/dev/fb0", Preview: ":8080", PNGDir: "/tmp/frames"}
	p, err := planFrom(o, config.Default())
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Displays) != 3 {
		t.Errorf("displays = %v, want all three", p.Displays)
	}
}

// The state directory is what makes /api/logs work. Without it the one
// endpoint that explains a failure over the network answers 404, and the only
// remaining option is the card.
func TestWebOptionsCarryTheStateDirectory(t *testing.T) {
	p, err := planFrom(serving(t), config.Default())
	if err != nil {
		t.Fatal(err)
	}
	if p.Web == nil {
		t.Fatal("no web server planned; the mirror cannot be diagnosed remotely")
	}
	if p.Web.StateDir != "/boot" {
		t.Errorf("web StateDir = %q, want /boot — /api/logs would answer 404", p.Web.StateDir)
	}
	if p.Web.ConfigPath != "/boot/config.json" {
		t.Errorf("web ConfigPath = %q, want the real config path", p.Web.ConfigPath)
	}
	if p.Web.Version != version {
		t.Errorf("web Version = %q, want %q", p.Web.Version, version)
	}
}

func TestWebCanBeTurnedOff(t *testing.T) {
	cfg := config.Default()
	cfg.Web.Enabled = false

	p, err := planFrom(serving(t), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if p.Web != nil {
		t.Error("web server planned despite being disabled in the configuration")
	}
}

// Every field of update.Options is a way for a fix to fail to arrive.
func TestUpdateOptionsComeFromTheConfiguration(t *testing.T) {
	cfg := config.Default()
	cfg.Update.Enabled = true
	cfg.Update.Repo = "someone/mirror"
	cfg.Update.Channel = "test"
	cfg.Update.AllowOS = true

	p, err := planFrom(serving(t), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if p.Update == nil {
		t.Fatal("no updater planned; the mirror can never be fixed remotely")
	}
	if p.Update.Repo != "someone/mirror" {
		t.Errorf("repo = %q, want the configured one", p.Update.Repo)
	}
	if p.Update.Channel != "test" {
		t.Errorf("channel = %q, want the configured one", p.Update.Channel)
	}
	// This one was unreachable for the whole life of the feature: the option
	// existed, and nothing ever set it, so no device could install a kernel.
	if !p.Update.AllowOS {
		t.Error("AllowOS did not reach the updater; system updates would silently never install")
	}
	if p.Update.StateDir != "/boot" {
		t.Errorf("update StateDir = %q, want /boot", p.Update.StateDir)
	}
}

// Configured to update, but with nowhere to put a download. The failure is
// silent, which is the worst property a remote fix channel can have.
func TestUpdateNeedsSomewhereToInstall(t *testing.T) {
	o := serving(t)
	o.StateDir = ""
	cfg := config.Default()
	cfg.Update.Enabled = true

	p, err := planFrom(o, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if p.Update != nil {
		t.Fatal("planned an updater with no state directory")
	}

	reason := p.UpdateSkippedReason(cfg, o)
	if !strings.Contains(reason, "state directory") {
		t.Errorf("reason = %q, want it to name the missing state directory", reason)
	}
}

func TestUpdateDisabledSaysSo(t *testing.T) {
	o := serving(t)
	cfg := config.Default()
	cfg.Update.Enabled = false

	p, err := planFrom(o, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if p.Update != nil {
		t.Fatal("planned an updater that the configuration disabled")
	}
	if reason := p.UpdateSkippedReason(cfg, o); !strings.Contains(reason, "disabled") {
		t.Errorf("reason = %q, want it to say the configuration disabled it", reason)
	}
}

// A running updater must never explain itself as skipped, or the log tells
// somebody the opposite of what the device is doing.
func TestRunningUpdaterHasNoSkipReason(t *testing.T) {
	o := serving(t)
	cfg := config.Default()
	cfg.Update.Enabled = true

	p, err := planFrom(o, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if reason := p.UpdateSkippedReason(cfg, o); reason != "" {
		t.Errorf("a planned updater reported itself skipped: %q", reason)
	}
}

// The portal writes credentials. Without a path to write them to, setup mode
// cannot do the one thing it exists for, so it must not claim to run.
func TestPortalNeedsBothAnInterfaceAndACredentialsPath(t *testing.T) {
	cases := []struct {
		name, iface, wpa string
		want             bool
	}{
		{"both", "wlan0", "/boot/wpa_supplicant.conf", true},
		{"no credentials path", "wlan0", "", false},
		{"no interface", "", "/boot/wpa_supplicant.conf", false},
		{"neither", "", "", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			o := serving(t)
			o.WiFiIface, o.WPAConf = tc.iface, tc.wpa
			p, err := planFrom(o, config.Default())
			if err != nil {
				t.Fatal(err)
			}
			if p.RunPortal != tc.want {
				t.Errorf("RunPortal = %v, want %v", p.RunPortal, tc.want)
			}
		})
	}
}

// The fetch gate waits for something to report the link. With no supervisor
// there is nothing to report it, so gating would mean waiting out the grace
// period on every start for no reason — including on a laptop.
func TestFetchGateFollowsTheSupervisor(t *testing.T) {
	withRadio := serving(t)
	p, err := planFrom(withRadio, config.Default())
	if err != nil {
		t.Fatal(err)
	}
	if !p.RunWiFiSupervisor || !p.GateFirstFetch {
		t.Error("a mirror with a radio neither supervises it nor gates its first fetch")
	}

	laptop := cliOptions{PNGDir: "/tmp/frames"}
	p, err = planFrom(laptop, config.Default())
	if err != nil {
		t.Fatal(err)
	}
	if p.RunWiFiSupervisor {
		t.Error("planned a wifi supervisor with no interface to supervise")
	}
	if p.GateFirstFetch {
		t.Error("a run with no supervisor still gates its first fetch, so it waits for nobody")
	}
}

func TestHealthOptionsCarryTheStateDirAndWatchdog(t *testing.T) {
	p, err := planFrom(serving(t), config.Default())
	if err != nil {
		t.Fatal(err)
	}
	// Without the state directory the failure counter is never cleared, and
	// mm-supervise reverts a build that works.
	if p.Health.StateDir != "/boot" {
		t.Errorf("health StateDir = %q, want /boot", p.Health.StateDir)
	}
	if p.Health.WatchdogPath != "/dev/watchdog" {
		t.Errorf("health WatchdogPath = %q, want /dev/watchdog", p.Health.WatchdogPath)
	}
}

// The config the device falls back to must still produce a reachable,
// updatable mirror — it is what runs when its own config cannot be read.
func TestDefaultConfigStillPlansBothLifelines(t *testing.T) {
	p, err := planFrom(serving(t), config.Default())
	if err != nil {
		t.Fatal(err)
	}
	if p.Web == nil {
		t.Error("a mirror on defaults serves no web UI, so it cannot be diagnosed")
	}
	if p.Update == nil {
		t.Error("a mirror on defaults never self-updates, so it cannot be fixed")
	}
}
