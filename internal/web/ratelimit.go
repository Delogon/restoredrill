package web

import (
	"net"
	"net/http"
	"sync"
	"time"
)

// rateLimiter is a simple minimum-interval limiter keyed by string. It guards
// "Run now" so a click storm (or a stuck script) cannot queue endless runs.
type rateLimiter struct {
	mu   sync.Mutex
	last map[string]time.Time
}

func newRateLimiter() *rateLimiter {
	return &rateLimiter{last: map[string]time.Time{}}
}

// allow reports whether key may act now, given a minimum interval between
// actions. It records the action when allowed.
func (l *rateLimiter) allow(key string, interval time.Duration) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	if t, ok := l.last[key]; ok && now.Sub(t) < interval {
		return false
	}
	l.last[key] = now
	return true
}

// clientIP extracts the remote IP (never a proxy header: those are spoofable
// unless the whole auth mode is trusted_header).
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// checkRunRateLimit applies the per-drill and per-client limits for manual
// triggers. It writes a 429 response and returns false when limited.
func (s *Server) checkRunRateLimit(w http.ResponseWriter, r *http.Request, drill string) bool {
	if !s.limiter.allow("drill:"+drill, 5*time.Second) {
		w.Header().Set("Retry-After", "5")
		http.Error(w, "a manual run of this drill was triggered very recently — wait a few seconds", http.StatusTooManyRequests)
		return false
	}
	if !s.limiter.allow("ip:"+clientIP(r), time.Second) {
		w.Header().Set("Retry-After", "1")
		http.Error(w, "too many run requests — slow down", http.StatusTooManyRequests)
		return false
	}
	return true
}