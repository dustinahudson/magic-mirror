// Command magicmirror renders the mirror.
//
// It runs identically on a Pi Zero W writing to /dev/fb0 and on a laptop
// serving a live preview over HTTP. Same widget code, same compositor, same
// frames — which is what makes layout iteration a page refresh instead of an
// SD card flash.
package main

import (
	"context"
	"flag"
	"fmt"
	"image"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	// Embed the IANA timezone database (~450KB) so time.LoadLocation works
	// without tzdata in the rootfs. Shipping tzdata as a Buildroot package
	// would cost more and adds something that can be missing at runtime —
	// which would silently drop every ICS event carrying a TZID to the
	// default zone.
	_ "time/tzdata"

	"github.com/dustinahudson/magic-mirror/internal/config"
	"github.com/dustinahudson/magic-mirror/internal/display"
	"github.com/dustinahudson/magic-mirror/internal/health"
	"github.com/dustinahudson/magic-mirror/internal/mdns"
	"github.com/dustinahudson/magic-mirror/internal/mirror"
	"github.com/dustinahudson/magic-mirror/internal/netmon"
	"github.com/dustinahudson/magic-mirror/internal/provision"
	"github.com/dustinahudson/magic-mirror/internal/render"
	"github.com/dustinahudson/magic-mirror/internal/source"
	"github.com/dustinahudson/magic-mirror/internal/store"
	"github.com/dustinahudson/magic-mirror/internal/update"
	"github.com/dustinahudson/magic-mirror/internal/web"
	"github.com/dustinahudson/magic-mirror/internal/widget"
)

// version is stamped at build time with -ldflags "-X main.version=...".
var version = "dev"

// firstFetchGrace bounds how long the calendar and weather wait for the link
// before trying anyway.
//
// Long enough to cover association, DHCP and the first DNS answer on a cold
// boot; short enough that a mirror whose supervisor is wrong about the network
// still fills its widgets within a minute or two rather than never.
const firstFetchGrace = 90 * time.Second

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "magicmirror:", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		configPath = flag.String("config", "config.json", "path to config.json")
		fbPath     = flag.String("fb", "", "framebuffer device (e.g. /dev/fb0); empty to skip")
		previewOn  = flag.String("preview", "", "serve a live preview on this address (e.g. :8080)")
		pngDir     = flag.String("png", "", "write each frame as a PNG into this directory")
		size       = flag.String("size", "1920x1080", "frame size when no framebuffer is open")
		stateDir   = flag.String("state", "", "directory for health marker and failure counter")
		wifiIface  = flag.String("wifi", "", "wireless interface to supervise (e.g. wlan0)")
		wpaConf    = flag.String("wpa-conf", "", "path to wpa_supplicant.conf; enables the setup portal")
		watchdog   = flag.String("watchdog", "", "hardware watchdog device (e.g. /dev/watchdog)")
		tick       = flag.Duration("tick", time.Second, "render tick interval")
		showVer    = flag.Bool("version", false, "print version and exit")
		verbose    = flag.Bool("v", false, "verbose logging")
	)
	flag.Parse()

	if *showVer {
		fmt.Println(version)
		return nil
	}

	level := slog.LevelInfo
	if *verbose {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
	log.Info("magic mirror starting", "version", version)

	cfg, warnings, err := loadConfig(*configPath, log)
	if err != nil {
		return err
	}
	for _, w := range warnings {
		log.Warn("config", "warning", w)
	}

	loc, err := cfg.Location()
	if err != nil {
		log.Warn("timezone", "err", err, "using", loc.String())
	}

	opts := cliOptions{
		ConfigPath: *configPath,
		FBPath:     *fbPath,
		Preview:    *previewOn,
		PNGDir:     *pngDir,
		Size:       *size,
		StateDir:   *stateDir,
		WiFiIface:  *wifiIface,
		WPAConf:    *wpaConf,
		Watchdog:   *watchdog,
		Tick:       *tick,
	}
	// Decide everything that can be decided before touching hardware. See
	// wiring.go for why these answers are worth having in one place.
	p, err := planFrom(opts, cfg)
	if err != nil {
		return err
	}
	if reason := p.UpdateSkippedReason(cfg, opts); reason != "" {
		// Said out loud, because the alternative is wondering months later
		// why a published fix never reached the device.
		log.Warn("self-update will not run", "reason", reason)
	}

	// Assemble the display stack. Multiple backends can run at once, so a
	// device can drive its panel and serve a preview simultaneously.
	bounds, err := parseSize(*size)
	if err != nil {
		return err
	}

	// A display that fails to open is a fault to report, not a reason to exit.
	//
	// Asking for no display at all is a mistake in the command line and stays
	// fatal. A display that was asked for and could not be opened is a
	// different thing entirely: exiting there produces a mirror that is broken
	// *and* silent — no web UI, no logs, no update channel — and the restart
	// loop that follows rolls back to a build with the same problem. Carrying
	// on leaves a device that can be asked what went wrong, and that can be
	// sent a fix.
	//
	// The render loop keeps running either way, which also keeps the watchdog
	// fed. A mirror rendering to nothing is not useful, but it is reachable,
	// and reachable is what decides whether anybody has to drive anywhere.
	var sinks display.Tee
	if *fbPath != "" {
		fb, err := display.OpenFrameBuffer(*fbPath)
		if err != nil {
			log.Error("framebuffer unavailable; carrying on so the mirror stays reachable",
				"path", *fbPath, "err", err)
		} else {
			defer fb.Close()
			log.Info("framebuffer open", "path", *fbPath, "mode", fb.Describe())
			bounds = fb.Bounds()
			sinks = append(sinks, fb)
		}
	}
	if *previewOn != "" {
		pv, err := display.NewPreview(*previewOn, bounds)
		if err != nil {
			log.Error("preview unavailable; carrying on", "addr", *previewOn, "err", err)
		} else {
			defer pv.Close()
			log.Info("preview serving", "url", "http://localhost"+*previewOn)
			sinks = append(sinks, pv)
		}
	}
	if *pngDir != "" {
		pw, err := display.NewPNGWriter(*pngDir, bounds, 0)
		if err != nil {
			log.Error("frame directory unavailable; carrying on", "dir", *pngDir, "err", err)
		} else {
			defer pw.Close()
			log.Info("writing frames", "dir", *pngDir)
			sinks = append(sinks, pw)
		}
	}
	if len(sinks) == 0 {
		// Worth saying loudly and exactly once: every backend that was asked
		// for failed, so nothing is being drawn anywhere.
		log.Error("no display backend opened; the mirror is running blind",
			"planned", strings.Join(p.Displays, ","))
	}

	data := store.New()
	fonts := render.NewFontSet()
	comp := mirror.NewCompositor(bounds, log)
	comp.SetGrid(cfg.Layout.Grid())
	comp.SetPlacements(buildPlacements(cfg))

	ctx, stop := signal.NotifyContext(context.Background(),
		os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Fetchers run on their own goroutines. Nothing below this line can
	// block the render loop, and nothing in the render loop can reach them.
	mgr := source.NewManager(data, log)
	for _, f := range buildSources(cfg, loc) {
		mgr.Add(f)
	}

	// Hold the calendar and weather until the link actually carries traffic.
	//
	// The wifi supervisor below is the thing that knows, but it is built after
	// this point, so the channel is made here and closed from its callback.
	// Without a supervisor there is nobody to ask — a laptop preview run, say —
	// so the gate opens immediately and nothing changes.
	netReady := make(chan struct{})
	var netOnce sync.Once
	markNetworkUp := func() { netOnce.Do(func() { close(netReady) }) }
	if p.GateFirstFetch {
		mgr.WaitForNetwork(netReady, firstFetchGrace)
	} else {
		markNetworkUp()
	}

	fetchDone := make(chan struct{})
	go func() {
		defer close(fetchDone)
		mgr.Run(ctx)
	}()

	// Setup portal. Watches for "we never got on the network" and turns the
	// mirror into an access point serving a setup page, so a changed wifi
	// password does not mean finding a card reader.
	//
	// The overlay is installed unconditionally: it renders nothing unless
	// the coordinator reports portal mode, and it is what stops setup mode
	// from looking like a broken mirror.
	comp.SetOverlay(&widget.Setup{})

	// Print the mirror's web address in the corner of every frame.
	//
	// Without it, reaching the settings page means already knowing the
	// address — and the settings page is where the address would be. Someone
	// who cannot get there has to go looking through the router's client
	// list, which is exactly the point at which a wall display stops being
	// something a household can own.
	//
	// The IP rather than the .local name: the name is the nicer thing to
	// type, but Android resolves it unreliably, and a phone is what people
	// have in their hand when they walk up to a mirror. The IP always works.
	comp.SetAddress(func() string {
		sys, ok := store.Get[source.SystemInfo](data.Load(), source.KeySystem).Get()
		if !ok || sys.IP == "" {
			return ""
		}
		return "http://" + sys.IP
	})

	var coord *provision.Coordinator
	if p.RunPortal {
		portal := provision.New(*wifiIface, *wpaConf, log)
		coord = provision.NewCoordinator(portal, log)
		coord.OnState = func(st provision.State) {
			data.Success(provision.KeyProvision, st)
		}
		go coord.Run(ctx)
		log.Info("setup portal supervisor started",
			"interface", *wifiIface, "credentials", *wpaConf)
	}

	// The wifi supervisor climbs a recovery ladder that ends at reboot
	// rather than starting there. It publishes into the same store the
	// widgets read, so link trouble shows on screen.
	if p.RunWiFiSupervisor {
		sup := netmon.New(*wifiIface, log)
		// While the setup portal owns the radio there is no route anywhere
		// by design, so recovery must stand down rather than fight it.
		if coord != nil {
			sup.Suspended = func() bool {
				return coord.State().Mode == provision.ModePortal
			}
		}
		sup.OnStatus = func(st netmon.Status) {
			if st.Up {
				// The supervisor's probe resolves a name and completes a TCP
				// handshake, which is a stronger statement than "associated"
				// and exactly what a fetch is about to need.
				markNetworkUp()
				data.Success(netmon.KeyNetwork, st)
				return
			}
			data.Failure(netmon.KeyNetwork, st.LastErr)
		}
		go sup.Run(ctx)
		log.Info("wifi supervisor started", "interface", *wifiIface)
	}

	// Answer to <hostname>.local on whatever network we join. Costs nothing
	// in image size, unlike shipping avahi and D-Bus to answer one question
	// about one name.
	responder := mdns.New(cfg.Hostname, log)
	go responder.Run(ctx)

	// Config changes are staged here and picked up by the render loop. The
	// web server never touches the compositor.
	applier := web.NewApplier(cfg)
	if p.Web != nil {
		wopts := *p.Web
		// The setup portal borrows this listener rather than opening its
		// own on the same port, and lends the settings page the one button
		// that can send the mirror back to it.
		if coord != nil {
			wopts.Portal = coord.Mux
			wopts.ForgetWiFi = coord.Forget
		}
		ws, err := web.New(wopts, applier, data, log)
		if err != nil {
			// A busy port must not stop the mirror rendering. The config
			// page is how you fix things, but the display is the product.
			log.Error("config web server unavailable", "err", err)
		} else {
			defer ws.Close()
			log.Info("config UI serving", "addr", ws.Addr())
		}
	}

	// Self-update. Installing exits cleanly rather than rebooting: init
	// respawns mm.current, which is now the new binary. If it fails to
	// reach healthy three times, mm-supervise restores mm.previous.
	if p.Update != nil {
		up := update.New(*p.Update, log)
		up.RequestRestart = func(reason string) {
			log.Info("restarting to run the new build", "reason", reason)
			stop()
		}
		go up.Run(ctx)
	}

	// The watchdog is petted from the render loop and nowhere else, so
	// "frames stopped" reboots and "network stalled" does not.
	hm := health.New(p.Health, log)
	defer hm.Close()

	reconfigureSources := func(c config.Config, l *time.Location) {
		mgr.Reconfigure(buildSources(c, l))
	}
	err = renderLoop(ctx, log, comp, sinks, data, fonts, applier, responder,
		reconfigureSources, loc, hm, *tick)

	// Give fetchers a moment to notice cancellation, but do not wait on a
	// slow server to exit — the process must always be able to stop.
	select {
	case <-fetchDone:
	case <-time.After(2 * time.Second):
		log.Warn("fetchers still running at shutdown; exiting anyway")
	}
	return err
}

// buildSources turns config into the set of fetchers to run.
//
// Sources are shared rather than per-widget: one fetcher per unique source
// regardless of how many widgets consume it, so two calendar tiles do not
// double-fetch the same ICS feeds.
func buildSources(cfg config.Config, loc *time.Location) []source.Fetcher {
	out := []source.Fetcher{
		source.NewSystem(version, 15*time.Second),
	}

	var fixed *source.Location
	if cfg.Weather.HasCoords() {
		fixed = &source.Location{
			Latitude:  cfg.Weather.Latitude,
			Longitude: cfg.Weather.Longitude,
		}
	}
	if cfg.Weather.Zipcode != "" || fixed != nil {
		out = append(out, source.NewWeather(
			cfg.Weather.Zipcode, cfg.Units == "metric", fixed, 15*time.Minute))
	}

	feeds := make([]source.Feed, 0, len(cfg.Calendars))
	for _, f := range cfg.Calendars {
		if f.URL == "" {
			continue
		}
		feeds = append(feeds, source.Feed{ID: f.ID, URL: f.URL, Name: f.Name, Color: f.Color})
	}
	if len(feeds) > 0 {
		out = append(out, source.NewCalendar(feeds, loc, 5*time.Minute))
	}

	return out
}

// renderLoop is the only goroutine that touches the frame.
//
// Everything it needs arrives through an atomic snapshot load. There is no
// HTTP client in scope, no source handle, and no lock it can block on — so a
// hung API cannot stall a frame. That is the entire point of the rewrite: in
// v1 this loop and the calendar fetch were the same code path.
func renderLoop(
	ctx context.Context,
	log *slog.Logger,
	comp *mirror.Compositor,
	sinks display.Display,
	data *store.Store,
	fonts *render.FontSet,
	applier *web.Applier,
	responder *mdns.Responder,
	// reconfigureSources swaps the fetcher set when the config changes. A
	// function rather than the Manager itself, to keep the promise above:
	// it takes a lock and returns, it never waits on a fetch.
	reconfigureSources func(config.Config, *time.Location),
	loc *time.Location,
	hm *health.Monitor,
	tick time.Duration,
) error {
	ticker := time.NewTicker(tick)
	defer ticker.Stop()

	var frames, presents uint64
	report := time.NewTicker(time.Minute)
	defer report.Stop()

	cfg := applier.Current()

	for {
		start := time.Now()

		// Pick up a configuration staged by the web UI. Rebuilding the
		// widget set here, on the render goroutine, is what lets config
		// apply live without a restart and without a data race.
		if next, ok := applier.Take(); ok {
			cfg = next
			if newLoc, err := cfg.Location(); err == nil {
				loc = newLoc
			}
			comp.SetGrid(cfg.Layout.Grid())
			comp.SetPlacements(buildPlacements(cfg))

			// Renaming the mirror has to take effect with everything else.
			// The responder read the hostname once at startup, so a rename
			// saved cleanly and changed nothing until the next reboot —
			// while the settings page stated the new name as fact.
			responder.Rename(cfg.Hostname)

			// And so do the things being fetched. The same mistake was here
			// for calendars and the weather location: the widget appeared,
			// nothing filled it, and the settings page said it was applied.
			reconfigureSources(cfg, loc)

			log.Info("applied new configuration",
				"widgets", len(cfg.Widgets), "calendars", len(cfg.Calendars))
		}

		wctx := widget.Context{
			Now:   start,
			Loc:   loc,
			Fonts: fonts,
			Data:  data.Load(), // atomic pointer load; wait-free
			Units: cfg.Units,
		}

		dirty := comp.Draw(wctx)
		frames++
		if len(dirty) > 0 {
			if err := sinks.Present(comp.Frame(), dirty); err != nil {
				// A failing sink must not stop the mirror. Log and carry on:
				// the preview dying is not a reason for the panel to stop.
				log.Error("present failed", "err", err)
			}
			presents++
		}

		// A frame completed. This is the only place the watchdog is petted,
		// and it is deliberately after the present rather than before the
		// draw: it attests that a frame actually reached the display.
		hm.Pet()

		if d := time.Since(start); d > tick {
			log.Warn("frame overran tick", "took", d, "tick", tick)
		}

		select {
		case <-ctx.Done():
			log.Info("shutting down", "frames", frames, "presents", presents)
			return nil
		case <-report.C:
			log.Info("render stats", "frames", frames, "presents", presents)
		case <-ticker.C:
		}
	}
}

// loadConfig reads the config, falling back to defaults when the file cannot
// be used.
//
// A missing config on first boot is normal, not an error — the mirror should
// come up showing something and let you fix it from the web UI.
//
// A corrupt config takes the same path, for a harder-won reason. Returning the
// error here exits the process, mm-supervise restarts it, the same bad bytes
// fail again, and the device sits at a blank screen with the explanation in a
// log file that is only reachable by pulling the card. Reporting a fault to
// somebody who cannot see the report is the same as not reporting it. Coming
// up on defaults keeps the screen lit and the web UI answering, which is what
// makes the config fixable in place.
//
// The bad file is deliberately left alone: it is the only copy of whatever the
// user had configured, and staying readable is what makes recovery possible.
func loadConfig(path string, log *slog.Logger) (config.Config, []string, error) {
	cfg, warnings, err := config.Load(path)
	if err == nil {
		return cfg, warnings, nil
	}
	if os.IsNotExist(errUnwrap(err)) {
		log.Warn("no config file; using defaults", "path", path)
		return config.Default(), nil, nil
	}
	// Before falling back to defaults, try the last-known-good copy. Defaults
	// mean somebody's calendars and location are gone from the screen without
	// anyone touching a setting; the previous config means losing only what
	// changed since the save before last.
	if prev := config.BackupPath(path); prev != path {
		if cfg, w, perr := config.Load(prev); perr == nil {
			log.Error("config unusable; running the last known good one instead",
				"path", path, "err", err, "using", prev)
			return cfg, w, nil
		}
	}

	log.Error("config unusable and no usable backup; starting on defaults so the mirror stays reachable",
		"path", path, "err", err)
	return config.Default(), warnings, nil
}

func errUnwrap(err error) error {
	for {
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return err
		}
		err = u.Unwrap()
	}
}

func buildPlacements(cfg config.Config) []mirror.Placement {
	out := make([]mirror.Placement, 0, len(cfg.Widgets))
	for _, inst := range cfg.Widgets {
		out = append(out, mirror.Placement{
			ID:     inst.ID,
			Type:   inst.Type,
			Pos:    inst.Pos,
			Widget: widget.Build(inst.Type, inst.Config),
		})
	}
	return out
}

func parseSize(s string) (image.Rectangle, error) {
	var w, h int
	if _, err := fmt.Sscanf(s, "%dx%d", &w, &h); err != nil {
		return image.Rectangle{}, fmt.Errorf("bad -size %q, want WxH", s)
	}
	if w <= 0 || h <= 0 {
		return image.Rectangle{}, fmt.Errorf("bad -size %q: must be positive", s)
	}
	return image.Rect(0, 0, w, h), nil
}
