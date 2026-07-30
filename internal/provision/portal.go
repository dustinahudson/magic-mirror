package provision

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"
)

// Server is the HTTP setup portal served while the AP is up.
//
// Kept deliberately small: one page, a scan list, and a form. Anything a
// phone browser needs to render without a network connection to fetch it
// from — no external CSS, no fonts, no framework.
type Server struct {
	portal   *Portal
	networks []Network

	mu       sync.Mutex
	saved    bool
	lastErr  string
	srv      *http.Server
	ln       net.Listener
	doneOnce sync.Once
	done     chan struct{}
}

// Serve starts the portal on addr. networks is the scan captured before the
// AP claimed the radio.
func Serve(addr string, p *Portal, networks []Network) (*Server, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("portal listen %s: %w", addr, err)
	}

	s := &Server{portal: p, networks: networks, ln: ln, done: make(chan struct{})}

	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleIndex)
	mux.HandleFunc("/api/networks", s.handleNetworks)
	mux.HandleFunc("POST /api/connect", s.handleConnect)

	// Captive-portal probes. Phones and laptops fetch one of these on
	// joining a network; answering with a redirect rather than the expected
	// body is what makes the setup sheet appear on its own.
	for _, probe := range []string{
		"/generate_204",              // Android
		"/gen_204",                   // Android
		"/hotspot-detect.html",       // iOS, macOS
		"/library/test/success.html", // iOS
		"/connecttest.txt",           // Windows
		"/ncsi.txt",                  // Windows
	} {
		mux.HandleFunc(probe, s.handleCaptiveProbe)
	}

	s.srv = &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = s.srv.Serve(ln) }()
	return s, nil
}

// Addr is the resolved listen address.
func (s *Server) Addr() string { return s.ln.Addr().String() }

// Done is closed once credentials have been accepted.
func (s *Server) Done() <-chan struct{} { return s.done }

// Close stops the portal.
func (s *Server) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return s.srv.Shutdown(ctx)
}

func (s *Server) handleCaptiveProbe(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "http://"+DefaultIP+"/", http.StatusFound)
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte(portalHTML))
}

func (s *Server) handleNetworks(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(s.networks)
}

func (s *Server) handleConnect(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SSID     string `json:"ssid"`
		Password string `json:"password"`
		Country  string `json:"country"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "could not read the form: "+err.Error())
		return
	}

	// Validation happens before anything is written, so a bad password
	// leaves a working config untouched.
	if err := s.portal.SaveCredentials(r.Context(), req.SSID, req.Password, req.Country); err != nil {
		s.mu.Lock()
		s.lastErr = err.Error()
		s.mu.Unlock()
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}

	s.mu.Lock()
	s.saved = true
	s.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"ok": true,
		"message": fmt.Sprintf(
			"Saved. The mirror is joining %q — this network will disappear in a moment.",
			req.SSID),
	})

	// Let the response reach the phone before the AP goes away underneath it.
	go func() {
		time.Sleep(2 * time.Second)
		s.doneOnce.Do(func() { close(s.done) })
	}()
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

const portalHTML = `<!doctype html>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Magic Mirror setup</title>
<style>
  :root { color-scheme: dark; }
  * { box-sizing: border-box; }
  body {
    margin: 0; padding: 1.25rem; background: #0b0b0d; color: #e6e6ec;
    font: 16px/1.55 system-ui, -apple-system, "Segoe UI", sans-serif;
    max-width: 30rem; margin-inline: auto;
  }
  h1 { font-size: 1.25rem; margin: .5rem 0 .25rem; }
  p.sub { color: #8b8b96; margin-top: 0; font-size: .9rem; }
  label { display: block; font-size: .8rem; color: #8b8b96; margin: 1rem 0 .3rem; }
  select, input, button {
    width: 100%; padding: .7rem; font: inherit;
    background: #131317; color: #e6e6ec;
    border: 1px solid #24242c; border-radius: 8px;
  }
  button {
    margin-top: 1.25rem; background: #6ea8fe; color: #06111f;
    font-weight: 600; border: none;
  }
  button[disabled] { opacity: .5; }
  .msg { margin-top: 1rem; padding: .75rem; border-radius: 8px; font-size: .9rem; display: none; }
  .msg.err { display: block; border: 1px solid #f85149; color: #f85149; }
  .msg.ok  { display: block; border: 1px solid #3fb950; color: #3fb950; }
  .sig { color: #5a5a66; }
</style>

<h1>Magic Mirror setup</h1>
<p class="sub">Choose the network the mirror should join.</p>

<label for="ssid">Network</label>
<select id="ssid"><option>Scanning…</option></select>

<label for="password">Password</label>
<input id="password" type="password" autocomplete="off"
       placeholder="Leave blank for an open network">

<label for="country">Country code</label>
<input id="country" value="US" maxlength="2" autocapitalize="characters">

<button id="go">Connect</button>
<div class="msg" id="msg"></div>

<script>
const $ = id => document.getElementById(id);

fetch('/api/networks').then(r => r.json()).then(nets => {
  const sel = $('ssid');
  sel.replaceChildren();
  if (!nets || !nets.length) {
    sel.append(new Option('No networks found', ''));
    return;
  }
  for (const n of nets) {
    // Signal is dBm; -50 is excellent, -80 is marginal.
    const bars = n.signal > -55 ? '●●●' : n.signal > -70 ? '●●' : '●';
    sel.append(new Option(n.ssid + '  ' + bars + (n.secure ? '' : '  (open)'), n.ssid));
  }
}).catch(() => {
  $('ssid').replaceChildren(new Option('Could not scan', ''));
});

$('go').onclick = async () => {
  const msg = $('msg');
  msg.className = 'msg';
  $('go').disabled = true;

  try {
    const res = await fetch('/api/connect', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        ssid: $('ssid').value,
        password: $('password').value,
        country: $('country').value.toUpperCase(),
      }),
    });
    const body = await res.json();
    if (!res.ok) {
      msg.textContent = body.error || 'Could not save.';
      msg.className = 'msg err';
      $('go').disabled = false;
      return;
    }
    msg.textContent = body.message;
    msg.className = 'msg ok';
  } catch (e) {
    msg.textContent = 'Could not reach the mirror: ' + e.message;
    msg.className = 'msg err';
    $('go').disabled = false;
  }
};
</script>
`
