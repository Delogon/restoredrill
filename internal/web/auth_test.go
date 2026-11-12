package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/restoredrill/restoredrill/internal/config"
	"golang.org/x/crypto/bcrypt"
)

func TestCheckBindRefusesNonLoopbackWithoutAuth(t *testing.T) {
	for _, listen := range []string{"0.0.0.0:8484", ":8484", "192.168.1.10:8484", "example.org:8484"} {
		if err := CheckBind(listen, "none"); err == nil {
			t.Errorf("CheckBind(%q, none) must refuse", listen)
		}
	}
	for _, listen := range []string{"127.0.0.1:8484", "localhost:8484", "[::1]:8484"} {
		if err := CheckBind(listen, "none"); err != nil {
			t.Errorf("CheckBind(%q, none) must pass: %v", listen, err)
		}
	}
	// With auth, any bind is allowed.
	if err := CheckBind("0.0.0.0:8484", "basic"); err != nil {
		t.Errorf("with auth the bind must be allowed: %v", err)
	}
	if err := CheckBind("not-host-port", "none"); err == nil {
		t.Error("invalid listen address must be refused")
	}
}

func TestNewRefusesUnsafeBind(t *testing.T) {
	cfg := baseConfig(t)
	cfg.Server.Listen = "0.0.0.0:8484"
	cfg.Server.Auth.Mode = "none"
	if _, err := newServerForTest(t, cfg); err == nil {
		t.Fatal("New must refuse a non-loopback bind without auth")
	}
}

func TestBasicAuth(t *testing.T) {
	hash, err := bcrypt.GenerateFromPassword([]byte("correct-password"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	s, _ := fixture(t, func(cfg *config.Config) {
		cfg.Server.Auth = config.Auth{
			Mode:  "basic",
			Users: map[string]config.AuthUser{"admin": {PasswordHash: string(hash)}},
		}
	})
	h := s.Handler()

	// No credentials -> 401 challenge.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("no creds: status=%d", rec.Code)
	}
	if !strings.Contains(rec.Header().Get("WWW-Authenticate"), "Basic") {
		t.Fatalf("missing WWW-Authenticate challenge")
	}

	// Wrong password -> 401.
	req := httptest.NewRequest("GET", "/", nil)
	req.SetBasicAuth("admin", "wrong")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong password: status=%d", rec.Code)
	}

	// Unknown user -> 401.
	req = httptest.NewRequest("GET", "/", nil)
	req.SetBasicAuth("nobody", "correct-password")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unknown user: status=%d", rec.Code)
	}

	// Correct credentials -> 200.
	req = httptest.NewRequest("GET", "/", nil)
	req.SetBasicAuth("admin", "correct-password")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("correct creds: status=%d", rec.Code)
	}

	// Health stays open without credentials.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("healthz must stay open: %d", rec.Code)
	}

	// Metrics requires auth when auth is configured.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("metrics must require auth: %d", rec.Code)
	}
}

func TestTrustedHeaderAuth(t *testing.T) {
	s, _ := fixture(t, func(cfg *config.Config) {
		cfg.Server.Auth = config.Auth{
			Mode:           "trusted_header",
			Header:         "X-Forwarded-User",
			TrustedProxies: []string{"10.0.0.0/8"},
		}
	})
	h := s.Handler()

	// Request from an untrusted source with a spoofed header -> 403.
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "203.0.113.9:1234"
	req.Header.Set("X-Forwarded-User", "attacker")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("spoofed header from untrusted source: status=%d", rec.Code)
	}

	// Trusted proxy but no identity header -> 401.
	req = httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "10.1.2.3:1234"
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("missing identity header: status=%d", rec.Code)
	}

	// Trusted proxy with identity header -> 200.
	req = httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "10.1.2.3:1234"
	req.Header.Set("X-Forwarded-User", "alice")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("trusted proxy: status=%d", rec.Code)
	}
}

func TestTrustedHeaderDefaultHeaderName(t *testing.T) {
	s, _ := fixture(t, func(cfg *config.Config) {
		cfg.Server.Auth = config.Auth{
			Mode:           "trusted_header",
			TrustedProxies: []string{"127.0.0.1/32"},
		}
	})
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "127.0.0.1:5555"
	req.Header.Set("X-Forwarded-User", "alice")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("default header X-Forwarded-User must be used: %d", rec.Code)
	}
}

func TestAPITokenAuth(t *testing.T) {
	s, _ := fixture(t, func(cfg *config.Config) {
		cfg.Server.Auth = config.Auth{Mode: "basic", Users: map[string]config.AuthUser{
			"admin": {PasswordHash: "$2a$10$N9qo8uLOickgx2ZMRZoMyeIjZAgcfl7p92ldGxad68LJZdL17lhWy"},
		}}
	})
	s.apiToken = "test-api-token"
	h := s.Handler()

	// Bearer token works on the API.
	req := httptest.NewRequest("GET", "/api/v1/drills", nil)
	req.Header.Set("Authorization", "Bearer test-api-token")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("api token: status=%d", rec.Code)
	}

	// Wrong token -> 401 (basic challenge).
	req = httptest.NewRequest("GET", "/api/v1/drills", nil)
	req.Header.Set("Authorization", "Bearer wrong")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong api token: status=%d", rec.Code)
	}

	// Bearer token is NOT accepted on HTML pages (API only).
	req = httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "Bearer test-api-token")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("api token on HTML page must not authenticate: %d", rec.Code)
	}
}