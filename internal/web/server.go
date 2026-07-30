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
		ln:         ln,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /", s.handleIndex)
	mux.HandleFunc("GET /api/config", s.handleGetConfig)
	mux.HandleFunc("PUT /api/config", s.handlePutConfig)
	mux.HandleFunc("GET /api/widget-types", s.handleWidgetTypes)
	mux.HandleFunc("GET /api/status", s.handleStatus)
	mux.HandleFunc("GET /api/logs", s.handleLogs)

	s.srv = &http.Server{
		Handler:           s.route(mux),
		ReadHeaderTimeout: 10 * time.Second,
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
var uiETag = sync.OnceValue(func() string {
	body, err := uiFS.ReadFile("ui.html")
	if err != nil {
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
	body, err := uiFS.ReadFile("ui.html")
	if err != nil {
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

	s.applier.Stage(cfg)
	s.log.Info("configuration updated via web UI",
		"widgets", len(cfg.Widgets), "calendars", len(cfg.Calendars))

	writeJSON(w, http.StatusOK, map[string]any{
		"ok":       true,
		"warnings": warnings,
	})
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
