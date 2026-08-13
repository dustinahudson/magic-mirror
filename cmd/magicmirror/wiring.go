package main

import (
	"fmt"
	"time"

	"github.com/dustinahudson/magic-mirror/internal/config"
	"github.com/dustinahudson/magic-mirror/internal/health"
	"github.com/dustinahudson/magic-mirror/internal/update"
	"github.com/dustinahudson/magic-mirror/internal/web"
)

// This file holds what run() decides, separated from what run() does.
//
// The decisions are the part worth testing and the part that has gone wrong:
// whether the config UI is served at all, whether the updater is constructed,
// what options each is handed. Every one of those answers, gotten wrong, ends
// with somebody driving to the mirror — a device that cannot be reached and
// cannot update itself has no remaining remote failure mode to diagnose.
//
// Meanwhile run() keeps everything that needs a framebuffer, a radio or a
// goroutine, none of which a test can have.

// cliOptions is the command line, gathered so the wiring can be decided
// without reading package-level flag state.
type cliOptions struct {
	ConfigPath string
	FBPath     string
	Preview    string
	PNGDir     string
	Size       string
	StateDir   string
	WiFiIface  string
	WPAConf    string
	Watchdog   string
	Tick       time.Duration
}

// plan is what run() is about to assemble. A nil pointer means "do not build
// this", which is deliberately different from "build it disabled": the reason
// a component is absent is exactly what someone needs to know later.
type plan struct {
	// Displays names the backends to open, in order. Empty is an error.
	Displays []string

	// RunPortal supervises the setup access point, and RunWiFiSupervisor the
	// recovery ladder. Both need an interface to act on.
	RunPortal         bool
	RunWiFiSupervisor bool

	// GateFirstFetch holds the calendar and weather until something can say
	// the link is up. Only meaningful when there is a supervisor to ask.
	GateFirstFetch bool

	Web    *web.Options
	Update *update.Options
	Health health.Options
}

// planFrom decides what to build from the command line and the configuration.
func planFrom(o cliOptions, cfg config.Config) (plan, error) {
	var p plan

	if o.FBPath != "" {
		p.Displays = append(p.Displays, "framebuffer")
	}
	if o.Preview != "" {
		p.Displays = append(p.Displays, "preview")
	}
	if o.PNGDir != "" {
		p.Displays = append(p.Displays, "png")
	}
	if len(p.Displays) == 0 {
		return plan{}, fmt.Errorf("no display selected: pass -fb, -preview or -png")
	}

	// The portal rewrites credentials, so it needs somewhere to write them.
	// Without both, setup mode cannot do the one thing it exists for.
	p.RunPortal = o.WiFiIface != "" && o.WPAConf != ""
	p.RunWiFiSupervisor = o.WiFiIface != ""

	// With no supervisor there is nothing to report the link, so waiting
	// would mean waiting out the grace period on every start for no reason.
	p.GateFirstFetch = p.RunWiFiSupervisor

	if cfg.Web.Enabled {
		p.Web = &web.Options{
			Listen:     cfg.Web.Listen,
			ConfigPath: o.ConfigPath,
			Version:    version,
			// Carrying the state directory is what makes /api/logs work.
			// Without it the one endpoint that explains a failure remotely
			// answers 404, and the card comes out of the wall instead.
			StateDir: o.StateDir,
		}
	}

	// Self-update needs somewhere to put the binary it downloads. A mirror
	// configured to update but given no state directory silently never does,
	// which removes the only way to fix it without visiting it.
	if cfg.Update.Enabled && o.StateDir != "" {
		p.Update = &update.Options{
			Repo:     cfg.Update.Repo,
			StateDir: o.StateDir,
			Version:  version,
			Channel:  cfg.Update.Channel,
			AllowOS:  cfg.Update.AllowOS,
		}
	}

	p.Health = health.Options{
		StateDir:     o.StateDir,
		WatchdogPath: o.Watchdog,
	}

	return p, nil
}

// UpdateSkippedReason explains why self-update will not run, for a log line
// somebody reads months later while wondering why a fix never arrived.
func (p plan) UpdateSkippedReason(cfg config.Config, o cliOptions) string {
	if p.Update != nil {
		return ""
	}
	switch {
	case !cfg.Update.Enabled:
		return "disabled in the configuration"
	case o.StateDir == "":
		return "no state directory, so there is nowhere to install a new build"
	}
	return "unknown"
}
