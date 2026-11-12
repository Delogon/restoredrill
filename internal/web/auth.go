package web

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"net"
	"net/http"
	"strings"

	"golang.org/x/crypto/bcrypt"
)

const sessionCookie = "rd_session"

// dummyHash is compared against when a basic-auth user does not exist, so the
// response time does not reveal whether a username is valid.
var dummyHash = []byte("$2a$10$N9qo8uLOickgx2ZMRZoMyeIjZAgcfl7p92ldGxad68LJZdL17lhWy")

// bearerToken extracts a Bearer token from the Authorization header.
func bearerToken(r *http.Request) (string, bool) {
	h := r.Header.Get("Authorization")
	if !strings.HasPrefix(h, "Bearer ") {
		return "", false
	}
	return strings.TrimSpace(strings.TrimPrefix(h, "Bearer ")), true
}

// authBasic verifies HTTP Basic credentials against the configured bcrypt
// hashes. On failure it writes a 401 challenge and returns false.
func (s *Server) authBasic(w http.ResponseWriter, r *http.Request) bool {
	user, pass, ok := r.BasicAuth()
	if ok {
		if u, exists := s.cfg.Server.Auth.Users[user]; exists {
			if bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(pass)) == nil {
				return true
			}
		} else {
			// Constant-ish time: still run a bcrypt comparison.
			_ = bcrypt.CompareHashAndPassword(dummyHash, []byte(pass))
		}
	}
	w.Header().Set("WWW-Authenticate", `Basic realm="restoredrill", charset="UTF-8"`)
	http.Error(w, "authentication required", http.StatusUnauthorized)
	return false
}

// authTrustedHeader trusts the configured identity header only when the
// request comes from a trusted proxy CIDR. Anything else fails closed.
func (s *Server) authTrustedHeader(w http.ResponseWriter, r *http.Request) bool {
	if !s.fromTrustedProxy(r) {
		http.Error(w, "trusted_header auth: request is not from a trusted proxy", http.StatusForbidden)
		return false
	}
	header := s.cfg.Server.Auth.Header
	if header == "" {
		header = "X-Forwarded-User"
	}
	if r.Header.Get(header) == "" {
		http.Error(w, "trusted_header auth: identity header missing", http.StatusUnauthorized)
		return false
	}
	return true
}

func (s *Server) fromTrustedProxy(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	for _, cidr := range s.cfg.Server.Auth.TrustedProxies {
		_, network, err := net.ParseCIDR(cidr)
		if err == nil && network.Contains(ip) {
			return true
		}
	}
	return false
}

// csrfToken derives the token for a session ID: HMAC(secret, sessionID).
func (s *Server) csrfToken(sessionID string) string {
	mac := hmac.New(sha256.New, s.csrfSecret)
	mac.Write([]byte(sessionID))
	return hex.EncodeToString(mac.Sum(nil))
}

// csrfForRequest returns the CSRF token for the request's session cookie, or
// "" when there is no session.
func (s *Server) csrfForRequest(r *http.Request) string {
	c, err := r.Cookie(sessionCookie)
	if err != nil || c.Value == "" {
		return ""
	}
	return s.csrfToken(c.Value)
}

// csrfProtect wraps mutating handlers. A valid CSRF token (form field or
// X-CSRF-Token header) bound to the session cookie is required. Requests
// authenticated with the API token are exempt: they are not cookie-based and
// cannot be forged cross-site.
func (s *Server) csrfProtect(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.apiToken != "" {
			if tok, ok := bearerToken(r); ok && constantTimeEqual(tok, s.apiToken) {
				next(w, r)
				return
			}
		}
		want := s.csrfForRequest(r)
		got := r.FormValue("csrf_token")
		if got == "" {
			got = r.Header.Get("X-CSRF-Token")
		}
		if want == "" || got == "" || !constantTimeEqual(got, want) {
			http.Error(w, "CSRF token missing or invalid — reload the page and retry", http.StatusForbidden)
			return
		}
		next(w, r)
	}
}

func constantTimeEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}