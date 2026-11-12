package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/restoredrill/restoredrill/internal/store"
)

// postForm sends a form POST with the session cookie and optional CSRF token.
func postForm(s *Server, path string, form url.Values, withToken bool) *httptest.ResponseRecorder {
	if withToken {
		form.Set("csrf_token", s.csrfToken("testsession"))
	}
	req := httptest.NewRequest("POST", path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: "testsession"})
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec
}

func TestFormRunRequiresCSRF(t *testing.T) {
	s, _ := fixture(t, nil)
	// Without a token -> 403.
	rec := postForm(s, "/drills/alpha/run", url.Values{}, false)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("no CSRF token: status=%d", rec.Code)
	}
	// With a wrong token -> 403.
	rec = postForm(s, "/drills/alpha/run", url.Values{"csrf_token": {"wrong"}}, false)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("wrong CSRF token: status=%d", rec.Code)
	}
}

func TestFormRunWithCSRFTriggersRun(t *testing.T) {
	s, st := fixture(t, nil)
	rec := postForm(s, "/drills/alpha/run", url.Values{}, true)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status=%d body=%s", rec.Code, mustBody(t, rec))
	}
	if loc := rec.Header().Get("Location"); loc != "/drills/alpha" {
		t.Fatalf("redirect=%q", loc)
	}
	// The run executes asynchronously; wait for it to appear.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		runs, _ := st.ListRuns("alpha", 10, 0)
		for _, r := range runs {
			if r.Trigger == store.TriggerManual && r.Status != store.StatusRunning {
				return // success
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("manual run was not recorded")
}

func TestFormRunRateLimited(t *testing.T) {
	s, _ := fixture(t, nil)
	rec := postForm(s, "/drills/alpha/run", url.Values{}, true)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("first run: status=%d", rec.Code)
	}
	// Immediate second trigger of the same drill -> 429.
	rec = postForm(s, "/drills/alpha/run", url.Values{}, true)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("second run: status=%d", rec.Code)
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Fatal("429 must include Retry-After")
	}
}

func TestFormRunUnknownDrill(t *testing.T) {
	s, _ := fixture(t, nil)
	rec := postForm(s, "/drills/nope/run", url.Values{}, true)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d", rec.Code)
	}
}

func TestCSRFTokenBoundToSession(t *testing.T) {
	s, _ := fixture(t, nil)
	// A token from another session must not work.
	req := httptest.NewRequest("POST", "/drills/alpha/run", strings.NewReader("csrf_token="+s.csrfToken("other-session")))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: "testsession"})
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cross-session token: status=%d", rec.Code)
	}
}

func TestAPITokenBypassesCSRF(t *testing.T) {
	s, st := fixture(t, nil)
	s.apiToken = "test-api-token"
	req := httptest.NewRequest("POST", "/api/v1/drills/alpha/run", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer test-api-token")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%s", rec.Code, mustBody(t, rec))
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		runs, _ := st.ListRuns("alpha", 10, 0)
		for _, r := range runs {
			if r.Trigger == store.TriggerManual {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("API-triggered run was not recorded")
}

func TestAPIRunRequiresCSRFWithoutToken(t *testing.T) {
	s, _ := fixture(t, nil)
	req := httptest.NewRequest("POST", "/api/v1/drills/alpha/run", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: "testsession"})
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status=%d", rec.Code)
	}
}

func TestSessionCookieSet(t *testing.T) {
	s, _ := fixture(t, nil)
	req := httptest.NewRequest("GET", "/", nil)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	var found bool
	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookie {
			found = true
			if !c.HttpOnly || c.SameSite != http.SameSiteLaxMode {
				t.Fatalf("session cookie must be HttpOnly + SameSite=Lax: %+v", c)
			}
		}
	}
	if !found {
		t.Fatal("session cookie was not set")
	}
}