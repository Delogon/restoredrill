package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/restoredrill/restoredrill/internal/config"
	"github.com/restoredrill/restoredrill/internal/store"
)

// apiGet performs an API GET and decodes the envelope's data payload.
func apiGet(t *testing.T, s *Server, path string) (int, map[string]any, string) {
	t.Helper()
	rec := get(s, path)
	body := mustBody(t, rec)
	var env struct {
		Schema int             `json:"schema"`
		Data   json.RawMessage `json:"data"`
		Error  string          `json:"error"`
	}
	if rec.Code == http.StatusOK || rec.Code == http.StatusAccepted {
		if err := json.Unmarshal([]byte(body), &env); err != nil {
			t.Fatalf("%s: invalid JSON envelope: %v\n%s", path, err, body)
		}
		if env.Schema != 1 {
			t.Fatalf("%s: schema=%d", path, env.Schema)
		}
	}
	var data map[string]any
	_ = json.Unmarshal(env.Data, &data)
	return rec.Code, data, body
}

func TestAPIDrills(t *testing.T) {
	s, _ := fixture(t, nil)
	rec := get(s, "/api/v1/drills")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d", rec.Code)
	}
	body := mustBody(t, rec)
	var env struct {
		Schema int `json:"schema"`
		Data   []struct {
			Name    string `json:"name"`
			Running bool   `json:"running"`
			LastRun *struct {
				ID     string `json:"id"`
				Status string `json:"status"`
			} `json:"last_run"`
			LastSuccess *struct {
				ID string `json:"id"`
			} `json:"last_success"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(body), &env); err != nil {
		t.Fatal(err)
	}
	if env.Schema != 1 || len(env.Data) != 2 {
		t.Fatalf("envelope=%+v", env)
	}
	byName := map[string]int{}
	for i, d := range env.Data {
		byName[d.Name] = i
	}
	alpha := env.Data[byName["alpha"]]
	if alpha.LastRun == nil || alpha.LastRun.ID != "01A2" || alpha.LastRun.Status != "fail" {
		t.Fatalf("alpha last run: %+v", alpha.LastRun)
	}
	if alpha.LastSuccess == nil || alpha.LastSuccess.ID != "01A1" {
		t.Fatalf("alpha last success: %+v", alpha.LastSuccess)
	}
}

func TestAPIDrillResolved(t *testing.T) {
	s, _ := fixture(t, nil)
	code, data, body := apiGet(t, s, "/api/v1/drills/alpha")
	if code != http.StatusOK {
		t.Fatalf("status=%d", code)
	}
	if data["name"] != "alpha" {
		t.Fatalf("data=%+v", data)
	}
	if !strings.Contains(body, "config_yaml") || !strings.Contains(body, "files_exist") {
		t.Fatalf("resolved config missing")
	}
}

func TestAPIDrillNotFound(t *testing.T) {
	s, _ := fixture(t, nil)
	rec := get(s, "/api/v1/drills/nope")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d", rec.Code)
	}
	if body := mustBody(t, rec); !strings.Contains(body, `"schema":1`) || !strings.Contains(body, `"error"`) {
		t.Fatalf("error envelope: %s", body)
	}
}

func TestAPIDrillRunsPagination(t *testing.T) {
	s, _ := fixture(t, nil)
	code, data, _ := apiGet(t, s, "/api/v1/drills/alpha/runs?limit=1&offset=0")
	if code != http.StatusOK {
		t.Fatalf("status=%d", code)
	}
	if data["total"].(float64) != 2 || data["limit"].(float64) != 1 {
		t.Fatalf("pagination meta: %+v", data)
	}
	runs := data["runs"].([]any)
	if len(runs) != 1 || runs[0].(map[string]any)["id"] != "01A2" {
		t.Fatalf("runs: %+v", runs)
	}
	// Offset 1 returns the older run.
	_, data2, _ := apiGet(t, s, "/api/v1/drills/alpha/runs?limit=1&offset=1")
	runs2 := data2["runs"].([]any)
	if len(runs2) != 1 || runs2[0].(map[string]any)["id"] != "01A1" {
		t.Fatalf("offset runs: %+v", runs2)
	}
}

func TestAPIRunAndLogs(t *testing.T) {
	s, _ := fixture(t, nil)
	code, data, _ := apiGet(t, s, "/api/v1/runs/01A2")
	if code != http.StatusOK {
		t.Fatalf("status=%d", code)
	}
	run := data["run"].(map[string]any)
	if run["id"] != "01A2" || run["status"] != "fail" {
		t.Fatalf("run: %+v", run)
	}
	checks := data["checks"].([]any)
	if len(checks) != 1 {
		t.Fatalf("checks: %+v", checks)
	}

	rec := get(s, "/api/v1/runs/01A2/logs")
	if rec.Code != http.StatusOK {
		t.Fatalf("logs status=%d", rec.Code)
	}
	if body := mustBody(t, rec); !strings.Contains(body, "selected snapshot snap01A2") {
		t.Fatalf("logs missing events")
	}

	if rec := get(s, "/api/v1/runs/missing"); rec.Code != http.StatusNotFound {
		t.Fatalf("missing run status=%d", rec.Code)
	}
}

// TestSecretCanaryNeverLeaves asserts the plan's canary rule: a known fake
// secret never appears in API responses or HTML.
func TestSecretCanaryNeverLeaves(t *testing.T) {
	canary := "canary-secret-9f8e7d6c5b4a"
	s, st := fixture(t, func(cfg *config.Config) {
		cfg.Redactor.Add(canary)
		// The secret appears in repository env and a check message.
		repo := cfg.Repositories["r"]
		repo.Env = map[string]string{"RESTIC_PASSWORD": canary}
		cfg.Repositories["r"] = repo
	})
	_ = st.InsertEvent(eventWithSecret("01A2", canary))

	paths := []string{
		"/", "/drills/alpha", "/runs/01A2", "/runs/01A2/logs.txt",
		"/api/v1/drills", "/api/v1/drills/alpha", "/api/v1/drills/alpha/runs",
		"/api/v1/runs/01A2", "/api/v1/runs/01A2/logs",
	}
	for _, p := range paths {
		rec := get(s, p)
		body := mustBody(t, rec)
		if strings.Contains(body, canary) {
			t.Errorf("secret canary leaked in %s", p)
		}
	}
}

func eventWithSecret(runID, canary string) store.RunEvent {
	return store.RunEvent{RunID: runID, TS: fixedNow, Level: "error", Step: "restore", Message: "auth failed: " + canary}
}

func TestAPIRunBadJSON(t *testing.T) {
	s, _ := fixture(t, nil)
	s.apiToken = "tok"
	req := httptest.NewRequest("POST", "/api/v1/drills/alpha/run", strings.NewReader(`{invalid`))
	req.Header.Set("Authorization", "Bearer tok")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d", rec.Code)
	}
}

func TestAPIMethodNotAllowed(t *testing.T) {
	s, _ := fixture(t, nil)
	req := httptest.NewRequest("DELETE", "/api/v1/drills", nil)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status=%d", rec.Code)
	}
}

