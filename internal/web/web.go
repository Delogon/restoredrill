// Package web serves the dashboard, JSON API, metrics and health endpoints.
// Everything is server-rendered and embedded in the binary; reading works
// without JavaScript. The only mutating action is "Run now", protected by
// CSRF tokens (or an API token) and rate limiting.
package web

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/restoredrill/restoredrill/internal/config"
	"github.com/restoredrill/restoredrill/internal/metrics"
	"github.com/restoredrill/restoredrill/internal/runner"
	"github.com/restoredrill/restoredrill/internal/scheduler"
	"github.com/restoredrill/restoredrill/internal/store"
)

//go:embed templates assets
var embeddedFS embed.FS

// Server is the dashboard/API HTTP server.
type Server struct {
	cfg     *config.Config
	store   *store.Store
	runner  *runner.Runner
	sched   *scheduler.Scheduler
	metrics *metrics.Collector
	log     *slog.Logger
	runCtx  context.Context

	loc        *time.Location
	csrfSecret []byte
	apiToken   string
	limiter    *rateLimiter
	pages      map[string]*template.Template
	partials   *template.Template
	assets     fs.FS
	now        func() time.Time // injectable clock for tests
}

// New builds the server. It refuses to bind a non-loopback address without
// auth (fail closed), loads the optional API token and parses all templates.
// runCtx is the base context for dashboard-triggered runs (cancelled on
// shutdown). sched and mc may be nil.
func New(runCtx context.Context, cfg *config.Config, st *store.Store, rn *runner.Runner, sched *scheduler.Scheduler, mc *metrics.Collector, log *slog.Logger) (*Server, error) {
	if err := CheckBind(cfg.Server.Listen, cfg.Server.Auth.Mode); err != nil {
		return nil, err
	}
	loc := time.UTC
	if cfg.Server.Timezone != "" {
		l, err := time.LoadLocation(cfg.Server.Timezone)
		if err != nil {
			return nil, fmt.Errorf("server.timezone: %w", err)
		}
		loc = l
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return nil, err
	}
	var apiToken string
	if p := cfg.Server.Auth.APITokenFile; p != "" {
		b, err := os.ReadFile(p)
		if err != nil {
			return nil, fmt.Errorf("cannot read server.auth.api_token_file %q: %w", p, err)
		}
		apiToken = strings.TrimSpace(string(b))
		if apiToken == "" {
			return nil, fmt.Errorf("server.auth.api_token_file %q is empty", p)
		}
	}
	if log == nil {
		log = slog.Default()
	}
	s := &Server{
		cfg:        cfg,
		store:      st,
		runner:     rn,
		sched:      sched,
		metrics:    mc,
		log:        log,
		runCtx:     runCtx,
		loc:        loc,
		csrfSecret: secret,
		apiToken:   apiToken,
		limiter:    newRateLimiter(),
		now:        time.Now,
	}
	if err := s.parseTemplates(); err != nil {
		return nil, err
	}
	sub, err := fs.Sub(embeddedFS, "assets")
	if err != nil {
		return nil, err
	}
	s.assets = sub
	return s, nil
}

// CheckBind enforces the plan's rule: binding to any non-loopback address
// requires an auth mode. It is checked at startup, before listening.
func CheckBind(listen, authMode string) error {
	host, _, err := net.SplitHostPort(listen)
	if err != nil {
		return fmt.Errorf("server.listen %q is not host:port — e.g. 127.0.0.1:8484", listen)
	}
	if authMode == "none" && !isLoopback(host) {
		return fmt.Errorf("refusing to bind %q without auth — set server.auth.mode to basic or trusted_header, or bind to 127.0.0.1", listen)
	}
	return nil
}

func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// Handler returns the root HTTP handler with all middleware applied.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	// Health endpoints are always open (no sensitive data).
	mux.HandleFunc("GET /healthz", s.handleHealthz)
	mux.HandleFunc("GET /readyz", s.handleReadyz)

	// Static assets are open: they contain no secrets and are needed to
	// render error pages.
	mux.Handle("GET /assets/", http.StripPrefix("/assets/", http.FileServerFS(s.assets)))

	// Dashboard pages.
	mux.HandleFunc("GET /{$}", s.handleOverview)
	mux.HandleFunc("GET /drills/{name}", s.handleDrill)
	mux.HandleFunc("GET /runs/{id}", s.handleRun)
	mux.HandleFunc("GET /runs/{id}/live", s.handleLive)
	mux.HandleFunc("GET /runs/{id}/events", s.handleRunEvents)
	mux.HandleFunc("GET /runs/{id}/logs.txt", s.handleLogDownload)
	mux.HandleFunc("GET /events", s.handleOverviewEvents)
	mux.HandleFunc("GET /partials/overview", s.handleOverviewPartial)
	mux.HandleFunc("POST /drills/{name}/run", s.csrfProtect(s.handleFormRun))

	// JSON API.
	mux.HandleFunc("GET /api/v1/drills", s.apiDrills)
	mux.HandleFunc("GET /api/v1/drills/{name}", s.apiDrill)
	mux.HandleFunc("GET /api/v1/drills/{name}/runs", s.apiDrillRuns)
	mux.HandleFunc("POST /api/v1/drills/{name}/run", s.csrfProtect(s.apiRun))
	mux.HandleFunc("GET /api/v1/runs/{id}", s.apiRunGet)
	mux.HandleFunc("GET /api/v1/runs/{id}/logs", s.apiRunLogs)

	// Metrics: protected whenever auth is configured.
	metricsHandler := http.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.metrics == nil {
			http.Error(w, "metrics not configured", http.StatusNotFound)
			return
		}
		s.metrics.Handler().ServeHTTP(w, r)
	}))
	if s.cfg.Server.Auth.Mode != "none" {
		metricsHandler = s.requireAuth(metricsHandler)
	}
	mux.Handle("GET /metrics", metricsHandler)

	return s.securityHeaders(s.withSession(s.requireAuth(mux)))
}

// securityHeaders adds a strict CSP (no inline scripts or styles) and the
// usual hardening headers to every response.
func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy",
			"default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self' data:; "+
				"connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}

// withSession ensures a session cookie exists (used to bind CSRF tokens).
func (s *Server) withSession(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := r.Cookie(sessionCookie); err != nil {
			buf := make([]byte, 32)
			if _, err := rand.Read(buf); err == nil {
				http.SetCookie(w, &http.Cookie{
					Name:     sessionCookie,
					Value:    hex.EncodeToString(buf),
					Path:     "/",
					HttpOnly: true,
					SameSite: http.SameSiteLaxMode,
					Secure:   r.TLS != nil,
				})
			}
		}
		next.ServeHTTP(w, r)
	})
}

// requireAuth applies the configured auth mode. Health, assets and (in mode
// none) everything else pass through.
func (s *Server) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		if p == "/healthz" || p == "/readyz" || strings.HasPrefix(p, "/assets/") {
			next.ServeHTTP(w, r)
			return
		}
		// API token (Bearer) is accepted on the JSON API only.
		if s.apiToken != "" && strings.HasPrefix(p, "/api/v1/") {
			if tok, ok := bearerToken(r); ok && subtle.ConstantTimeCompare([]byte(tok), []byte(s.apiToken)) == 1 {
				next.ServeHTTP(w, r)
				return
			}
		}
		switch s.cfg.Server.Auth.Mode {
		case "none":
			next.ServeHTTP(w, r)
		case "basic":
			if s.authBasic(w, r) {
				next.ServeHTTP(w, r)
			}
		case "trusted_header":
			if s.authTrustedHeader(w, r) {
				next.ServeHTTP(w, r)
			}
		default:
			http.Error(w, "unknown auth mode", http.StatusInternalServerError)
		}
	})
}

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fmt.Fprintln(w, "ok")
}

func (s *Server) handleReadyz(w http.ResponseWriter, r *http.Request) {
	if _, err := s.store.CountRuns(""); err != nil {
		http.Error(w, "store not ready", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fmt.Fprintln(w, "ok")
}