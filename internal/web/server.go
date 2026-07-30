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
	"embed"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/dustinahudson/magic-mirror/internal/config"
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

	srv *http.Server
	ln  net.Listener
}

// Options configures a Server.
type Options struct {
	Listen     string
	ConfigPath string
	Version    string
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
		ln:         ln,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /", s.handleIndex)
	mux.HandleFunc("GET /api/config", s.handleGetConfig)
	mux.HandleFunc("PUT /api/config", s.handlePutConfig)
	mux.HandleFunc("GET /api/widget-types", s.handleWidgetTypes)
	mux.HandleFunc("GET /api/status", s.handleStatus)

	s.srv = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() { _ = s.srv.Serve(ln) }()
	return s, nil
}

// Addr is the resolved listen address.
func (s *Server) Addr() string { return s.ln.Addr().String() }

// Close stops the server.
func (s *Server) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return s.srv.Shutdown(ctx)
}

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
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
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
