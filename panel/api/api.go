// Package api holds the panel's HTTP handlers: the JSON API used by the web
// app, the agent endpoints, and the install script.
package api

import (
	"encoding/json"
	"errors"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/arman-arif/control-portal/panel/hub"
	"github.com/arman-arif/control-portal/panel/store"
)

type Config struct {
	PublicURL   string // base URL agents and browsers use to reach the panel
	WebDir      string // built web app (web/dist)
	AgentDir    string // directory holding keel-agent-linux-<arch> binaries
	AllowSignup bool   // allow sign-up after the first account exists
	TrustProxy  bool   // take the client IP from X-Forwarded-For
}

type API struct {
	cfg    Config
	store  *store.Store
	hub    *hub.Hub
	logins *rateLimiter
}

func New(cfg Config, st *store.Store, h *hub.Hub) *API {
	return &API{cfg: cfg, store: st, hub: h, logins: newRateLimiter(5, loginWindow)}
}

func (a *API) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/auth/status", a.handleAuthStatus)
	mux.HandleFunc("POST /api/auth/signup", a.handleSignup)
	mux.HandleFunc("POST /api/auth/login", a.handleLogin)
	mux.HandleFunc("POST /api/auth/logout", a.handleLogout)
	mux.Handle("GET /api/auth/me", a.requireUser(a.handleMe))

	mux.Handle("GET /api/servers", a.requireUser(a.handleListServers))
	mux.Handle("POST /api/servers", a.requireUser(a.handleCreateServer))
	mux.Handle("GET /api/servers/{id}", a.requireUser(a.handleGetServer))
	mux.Handle("DELETE /api/servers/{id}", a.requireUser(a.handleDeleteServer))
	mux.Handle("POST /api/servers/{id}/install-command", a.requireUser(a.handleInstallCommand))
	mux.Handle("GET /api/servers/{id}/stats", a.requireUser(a.handleServerStats))
	mux.Handle("POST /api/servers/{id}/services/{name}/{action}", a.requireUser(a.handleServiceAction))

	mux.HandleFunc("POST /api/agent/register", a.handleAgentRegister)
	mux.HandleFunc("GET /api/agent/ws", a.handleAgentWS)

	mux.HandleFunc("GET /install.sh", a.handleInstallScript)
	mux.HandleFunc("GET /downloads/{file}", a.handleDownload)

	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "not found")
	})
	mux.Handle("/", spaHandler(a.cfg.WebDir))

	return csrfGuard(mux)
}

// csrfGuard requires a custom header on state-changing browser API calls.
// Browsers cannot add custom headers to cross-site requests without a CORS
// preflight, which the panel never approves. Agent endpoints authenticate
// with their own tokens and are exempt.
func csrfGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead &&
			strings.HasPrefix(r.URL.Path, "/api/") && !strings.HasPrefix(r.URL.Path, "/api/agent/") &&
			r.Header.Get("X-Keel-Request") != "1" {
			writeError(w, http.StatusForbidden, "missing X-Keel-Request header")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// spaHandler serves the built web app, falling back to index.html so client
// side routes work on reload.
func spaHandler(dir string) http.Handler {
	files := http.FileServer(http.Dir(dir))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := filepath.Join(dir, filepath.FromSlash(filepath.Clean("/"+r.URL.Path)))
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			files.ServeHTTP(w, r)
			return
		}
		index := filepath.Join(dir, "index.html")
		if _, err := os.Stat(index); err != nil {
			http.Error(w, "web app not built; run `npm run build` in web/", http.StatusNotFound)
			return
		}
		http.ServeFile(w, r, index)
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func internalError(w http.ResponseWriter, err error) {
	log.Printf("internal error: %v", err)
	writeError(w, http.StatusInternalServerError, "internal error")
}

func readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return false
	}
	return true
}

func (a *API) clientIP(r *http.Request) string {
	if a.cfg.TrustProxy {
		if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
			ip, _, _ := strings.Cut(fwd, ",")
			return strings.TrimSpace(ip)
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func isNotFound(err error) bool { return errors.Is(err, store.ErrNotFound) }
