// Package web serves the configuration UI.
//
// Two properties matter more than the HTML:
//
//   - Nothing here touches the compositor. Config changes are staged through
//     an Applier and picked up by the render goroutine.
//   - A bad config never replaces a good one. Saves validate first, and a
//     rejected save leaves the mirror running exactly as it was — you cannot
//     lock yourself out through this page.
package web

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/dustinahudson/magic-mirror/internal/config"
	"github.com/dustinahudson/magic-mirror/internal/provision"
	"github.com/dustinahudson/magic-mirror/internal/store"
	"github.com/dustinahudson/magic-mirror/internal/widget"
)

//go:embed ui.html
var uiFS embed.FS

// Server is the config web server.
type Server struct {
	applier    *Applier
	data       *store.Store
	log        *slog.Logger
	configPath string
	version    string
	stateDir   string

	// portal, when set, takes precedence over the config UI while the
	// setup access point is running.
	portal *provision.Mux

	// forgetWiFi erases the saved network and sends the mirror back to setup
	// mode. Nil on a build that does not manage its own wifi.
	forgetWiFi func(context.Context) error

	srv *http.Server
	ln  net.Listener
}

// Options configures a Server.
type Options struct {
	Listen     string
	ConfigPath string
	Version    string

	// StateDir is where logs live, for the /api/logs endpoint.
	StateDir string

	// Portal lets the setup portal borrow this server's listener rather
	// than opening a second one on the same port.
	Portal *provision.Mux

	// ForgetWiFi backs the "forget the network" button. Injected rather than
	// reached for, so this package keeps knowing nothing about radios and a
	// test can watch it fire without one.
	ForgetWiFi func(context.Context) error
}

// New starts the config server.
func New(opts Options, applier *Applier, data *store.Store, log *slog.Logger) (*Server, error) {
	ln, err := net.Listen("tcp", opts.Listen)
	if err != nil {
		return nil, fmt.Errorf("web listen %s: %w", opts.Listen, err)
	}

	s := &Server{
		applier:    applier,
		data:       data,
		log:        log,
		configPath: opts.ConfigPath,
		version:    opts.Version,
		stateDir:   opts.StateDir,
		portal:     opts.Portal,
		forgetWiFi: opts.ForgetWiFi,
		ln:         ln,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /", s.handleIndex)
	mux.HandleFunc("GET /api/config", s.handleGetConfig)
	mux.HandleFunc("PUT /api/config", s.handlePutConfig)
	mux.HandleFunc("GET /api/widget-types", s.handleWidgetTypes)
	mux.HandleFunc("GET /api/status", s.handleStatus)
	mux.HandleFunc("GET /api/logs", s.handleLogs)
	mux.HandleFunc("POST /api/reset", s.handleReset)
	mux.HandleFunc("POST /api/forget-wifi", s.handleForgetWiFi)

	s.srv = &http.Server{
		Handler:           s.route(mux),
		ReadHeaderTimeout: 10 * time.Second,

		// Bound the rest of the exchange too, not just the headers.
		//
		// Every connection costs a goroutine and its buffers on a device with
		// 512MB, and there is no endpoint here that legitimately takes a long
		// time: the largest response is a quarter-megabyte log tail. Without
		// these, one phone that walks out of wifi range mid-request holds its
		// connection until the process restarts, and a handful of those is a
		// mirror that stops answering the only page that can fix it.
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 60 * time.Second,
		IdleTimeout:  90 * time.Second,
	}
	go func() { _ = s.srv.Serve(ln) }()
	return s, nil
}

// route hands requests to the setup portal while provisioning is active,
// and to the config UI otherwise.
//
// One server owns port 80 because a captive portal has to live there and
// cannot share. Serving the config page to a phone that joined
// MagicMirror-Setup would also be useless — there is nothing to configure
// until the mirror is on a network.
func (s *Server) route(configUI http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.portal != nil {
			if h := s.portal.Handler(); h != nil {
				h.ServeHTTP(w, r)
				return
			}
		}
		configUI.ServeHTTP(w, r)
	})
}

// Addr is the resolved listen address.
func (s *Server) Addr() string { return s.ln.Addr().String() }

// Close stops the server.
func (s *Server) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return s.srv.Shutdown(ctx)
}

// uiETag is the settings page's identity, computed once from its contents.
//
// The page is embedded in the binary, so it changes with every build — and
// the mirror updates itself, which means a browser can hold a copy of the
// page from a version the mirror is no longer running. Served with no
// validator at all, that copy is kept on heuristics and someone ends up
// driving an old settings page against a new API, with no way to tell.
// uiBody is the settings page, read out of the binary once.
//
// It never changes for the life of the process, and re-reading 30KB out of
// the embedded filesystem on every request is work a single ARMv6 core does
// not need to repeat — least of all while it is also compositing frames.
var uiBody = sync.OnceValue(func() []byte {
	body, err := uiFS.ReadFile("ui.html")
	if err != nil {
		return nil
	}
	return body
})

var uiETag = sync.OnceValue(func() string {
	body := uiBody()
	if body == nil {
		return ""
	}
	sum := sha256.Sum256(body)
	return `"` + hex.EncodeToString(sum[:8]) + `"`
})

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	body := uiBody()
	if body == nil {
		http.Error(w, "ui missing", http.StatusInternalServerError)
		return
	}

	// no-cache rather than no-store: the browser may keep the page, but it
	// has to ask first. On a LAN that revalidation is a round trip and a
	// 304, which is cheaper for a Pi Zero than re-sending 30KB every load.
	etag := uiETag()
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	if etag != "" {
		w.Header().Set("ETag", etag)
		if match := r.Header.Get("If-None-Match"); match == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
	}
	_, _ = w.Write(body)
}

func (s *Server) handleGetConfig(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.applier.Current())
}

// handlePutConfig validates, persists, then stages a new configuration.
//
// Order matters. Validation happens before anything is written, so a
// rejected config never reaches the disk or the render loop, and the mirror
// carries on with what it had.
func (s *Server) handlePutConfig(w http.ResponseWriter, r *http.Request) {
	var raw json.RawMessage
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&raw); err != nil {
		writeJSON(w, http.StatusBadRequest, apiError{
			Error: "could not read configuration: " + err.Error(),
		})
		return
	}

	cfg, warnings, err := config.Parse(raw)
	if err != nil {
		// The load-bearing case: reject and change nothing.
		writeJSON(w, http.StatusBadRequest, apiError{
			Error:    err.Error(),
			Warnings: warnings,
			Note:     "configuration rejected; the mirror is still running the previous one",
		})
		return
	}

	if s.configPath != "" {
		if err := config.Save(s.configPath, cfg); err != nil {
			writeJSON(w, http.StatusInternalServerError, apiError{
				Error: "could not save: " + err.Error(),
				Note:  "the mirror is still running the previous configuration",
			})
			return
		}
	}

	// Say which settings the running mirror will not pick up until it
	// restarts, before staging the change that makes the comparison stale.
	//
	// Worth the trouble because of how this fails otherwise. The page said
	// "the mirror has updated" whatever happened, so a setting that needed a
	// restart looked identical to one that did not: nothing changed, no
	// explanation, and the only thing left to try was pressing save again.
	// The logs from one afternoon show that being tried nine times.
	deferred := restartRequired(s.applier.Current(), cfg)

	s.applier.Stage(cfg)
	s.log.Info("configuration updated via web UI",
		"widgets", len(cfg.Widgets), "calendars", len(cfg.Calendars))
	if deferred != "" {
		s.log.Info("some settings need a restart to take effect", "settings", deferred)
	}

	out := map[string]any{
		"ok":       true,
		"warnings": warnings,
	}
	if deferred != "" {
		out["note"] = deferred + " take effect when the mirror restarts."
	}
	writeJSON(w, http.StatusOK, out)
}

// restartRequired names the settings that the running process reads once at
// startup, so a change to them is saved but not live.
//
// These are the two the mirror still builds from a copy taken at boot: the
// updater's options and the web listener. Everything else — widgets, layout,
// calendars, the weather location, the timezone, the hostname — is rebuilt
// when the render loop takes a staged config.
func restartRequired(running, next config.Config) string {
	var parts []string
	if running.Update != next.Update {
		parts = append(parts, "Software update settings")
	}
	if running.Web.Listen != next.Web.Listen || running.Web.Enabled != next.Web.Enabled {
		parts = append(parts, "The settings page address")
	}
	return strings.Join(parts, " and ")
}

// fromThisPage reports whether a request could have come from the settings
// page rather than from some other site the browser happens to have open.
//
// Nothing here is authenticated, and deliberately so: an appliance on a home
// LAN has no accounts to authenticate against, and a password on this page
// would be one more thing to lose. But "anyone on the network may change the
// mirror" must not quietly extend to "any web page anyone in the house is
// looking at may reset it".
//
// PUT /api/config gets this for free: a form cannot issue a PUT, and a
// cross-origin fetch that does is preflighted, which this server never
// answers. POST has no such protection — a form posts across origins without
// asking. What a form cannot do is set this content type, so requiring it
// puts these two endpoints behind the same door as the rest of the API.
func fromThisPage(r *http.Request) bool {
	ct := r.Header.Get("Content-Type")
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = ct[:i]
	}
	return strings.EqualFold(strings.TrimSpace(ct), "application/json")
}

// handleReset puts the configuration back to what the mirror shipped with.
//
// Same order as a save — write, then stage — so a reset that cannot reach the
// disk does not leave the running mirror and the file disagreeing about what
// its configuration is.
func (s *Server) handleReset(w http.ResponseWriter, r *http.Request) {
	if !fromThisPage(r) {
		writeJSON(w, http.StatusUnsupportedMediaType, apiError{
			Error: "expected a JSON request",
		})
		return
	}

	cfg := config.Default()
	if s.configPath != "" {
		if err := config.Save(s.configPath, cfg); err != nil {
			writeJSON(w, http.StatusInternalServerError, apiError{
				Error: "could not reset: " + err.Error(),
				Note:  "the mirror is still running the previous configuration",
			})
			return
		}
	}

	s.applier.Stage(cfg)
	s.log.Warn("configuration reset to defaults via web UI")

	// The new configuration comes back with the reply rather than leaving the
	// page to fetch it. /api/config reports what the mirror is *running*, and
	// a staged config is not running until the render loop takes it at the top
	// of a tick — so a page that asked immediately would be told the old
	// settings, redraw itself with them, and hand them straight back on the
	// next save, quietly undoing the reset.
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":     true,
		"config": cfg,
	})
}

// handleForgetWiFi erases the saved network so the mirror comes back as the
// setup access point.
//
// The reply is written before the work starts, because the work takes away the
// connection the reply travels over. Doing it the other way round leaves the
// browser showing a network error for an operation that in fact succeeded —
// the one outcome guaranteed to make someone go looking for a card reader,
// which is the thing the setup portal exists to avoid.
func (s *Server) handleForgetWiFi(w http.ResponseWriter, r *http.Request) {
	if s.forgetWiFi == nil {
		writeJSON(w, http.StatusNotFound, apiError{
			Error: "this mirror does not manage its own wifi",
		})
		return
	}
	if !fromThisPage(r) {
		writeJSON(w, http.StatusUnsupportedMediaType, apiError{
			Error: "expected a JSON request",
		})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"ok":   true,
		"ssid": provision.DefaultSSID,
	})
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}

	go func() {
		// Long enough for the reply above to be on the wire and read. A
		// second is imperceptible next to the ten or so the mirror then
		// spends scanning and bringing the access point up.
		time.Sleep(time.Second)

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := s.forgetWiFi(ctx); err != nil {
			// Nothing to report this to: the page that asked is on the far
			// side of the link we were about to drop. The log is what someone
			// reads afterwards to find out why the mirror stayed put.
			s.log.Error("could not return the mirror to setup mode", "err", err)
		}
	}()
}

// handleWidgetTypes returns the registry, which is what lets the UI render a
// form for every widget type without knowing anything about them.
func (s *Server) handleWidgetTypes(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, widget.Descriptors())
}

// sourceStatus is one data source's health, for the UI.
type sourceStatus struct {
	Key         string `json:"key"`
	Status      string `json:"status"`
	LastSuccess string `json:"lastSuccess,omitempty"`
	LastAttempt string `json:"lastAttempt,omitempty"`
	Age         string `json:"age,omitempty"`
	Error       string `json:"error,omitempty"`
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	snap := s.data.Load()
	now := time.Now()

	var out []sourceStatus
	for _, key := range snap.Keys() {
		e, ok := snap.Entry(key)
		if !ok {
			continue
		}
		st := sourceStatus{Key: key, Status: e.Status.String()}
		if !e.LastSuccess.IsZero() {
			st.LastSuccess = e.LastSuccess.Format(time.RFC3339)
			st.Age = e.Age(now).Round(time.Second).String()
		}
		if !e.LastAttempt.IsZero() {
			st.LastAttempt = e.LastAttempt.Format(time.RFC3339)
		}
		if e.LastErr != nil {
			st.Error = e.LastErr.Error()
		}
		out = append(out, st)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"version": s.version,
		"sources": out,
	})
}

// handleLogs serves the tail of the device's log files.
//
// Exists so the mirror can be diagnosed without pulling its SD card. Nearly
// every failure during bring-up was understood only after a card round trip,
// and twice the logs that would have explained it were destroyed by the
// power cycle that produced them. A device that can describe its own state
// over the network is worth a great deal more than one that cannot.
//
// Read-only, and bounded: only the tail, only known files.
func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	if s.stateDir == "" {
		http.Error(w, "no state directory configured", http.StatusNotFound)
		return
	}

	name := r.URL.Query().Get("file")
	if name == "" {
		name = "mm.log"
	}
	// Fixed set rather than a path parameter: this endpoint must never
	// become a way to read arbitrary files off the device.
	var path string
	switch name {
	case "mm.log", "network.log":
		path = filepath.Join(s.stateDir, "logs", name)
	case "messages":
		// syslog, which lives in RAM. Carries anything the init scripts and
		// daemons report that does not reach our own logs.
		path = "/var/log/messages"
	case "dmesg":
		path = "/var/log/dmesg"
	default:
		http.Error(w, "unknown log file", http.StatusBadRequest)
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		http.Error(w, "log unavailable: "+err.Error(), http.StatusNotFound)
		return
	}

	const maxBytes = 256 << 10
	if len(data) > maxBytes {
		data = data[len(data)-maxBytes:]
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(data)
}

type apiError struct {
	Error    string   `json:"error"`
	Warnings []string `json:"warnings,omitempty"`
	Note     string   `json:"note,omitempty"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}
