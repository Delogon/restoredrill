package notify

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/restoredrill/restoredrill/internal/config"
	"github.com/restoredrill/restoredrill/internal/redact"
	"github.com/restoredrill/restoredrill/internal/store"
)

// captureSender records messages instead of sending them.
type captureSender struct {
	msgs []Message
	err  error
}

func (c *captureSender) Send(ctx context.Context, m Message) error {
	c.msgs = append(c.msgs, m)
	return c.err
}

func testStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func testConfig() *config.Config {
	return &config.Config{
		Notifiers: map[string]config.Notifier{
			"cap": {Type: "webhook", URL: "http://example.invalid", On: []string{"fail", "error", "recovered", "pass"}},
		},
		Defaults: config.Defaults{Notify: []string{"cap"}},
		Drills: map[string]config.Drill{
			"d": {Repository: "r"},
		},
		Server:   config.Server{PublicURL: "https://drills.example.org"},
		Redactor: redact.New(),
	}
}

func insertRun(t *testing.T, st *store.Store, id, status string, started time.Time) store.Run {
	t.Helper()
	r := store.Run{ID: id, Drill: "d", Status: status, StartedAt: started, FinishedAt: started.Add(time.Second), SnapshotTime: started.Add(-time.Hour)}
	if err := st.InsertRun(r); err != nil {
		t.Fatal(err)
	}
	return r
}

func newDispatcher(t *testing.T, cfg *config.Config, st *store.Store) (*Dispatcher, *captureSender) {
	t.Helper()
	d := New(cfg, st, slog.New(slog.NewTextHandler(io.Discard, nil)))
	cap := &captureSender{}
	d.SetSender("cap", cap)
	return d, cap
}

func TestEventFail(t *testing.T) {
	st := testStore(t)
	cfg := testConfig()
	d, cap := newDispatcher(t, cfg, st)
	run := insertRun(t, st, "01A", store.StatusFail, time.Now())
	d.Notify(context.Background(), run, []store.CheckResult{{Name: "files_exist", Status: store.StatusFail, Message: "missing"}})
	if len(cap.msgs) != 1 || cap.msgs[0].Event != EventFail {
		t.Fatalf("expected one fail event, got %+v", cap.msgs)
	}
	if cap.msgs[0].Link != "https://drills.example.org/runs/01A" {
		t.Fatalf("link=%q", cap.msgs[0].Link)
	}
}

func TestEventRecovered(t *testing.T) {
	st := testStore(t)
	cfg := testConfig()
	d, cap := newDispatcher(t, cfg, st)
	insertRun(t, st, "01A", store.StatusFail, time.Now().Add(-2*time.Minute))
	run := insertRun(t, st, "01B", store.StatusPass, time.Now())
	d.Notify(context.Background(), run, nil)
	if len(cap.msgs) != 1 || cap.msgs[0].Event != EventRecovered {
		t.Fatalf("expected recovered, got %+v", cap.msgs)
	}
}

func TestEventPassAfterPass(t *testing.T) {
	st := testStore(t)
	cfg := testConfig()
	d, cap := newDispatcher(t, cfg, st)
	insertRun(t, st, "01A", store.StatusPass, time.Now().Add(-2*time.Minute))
	run := insertRun(t, st, "01B", store.StatusPass, time.Now())
	d.Notify(context.Background(), run, nil)
	if len(cap.msgs) != 1 || cap.msgs[0].Event != EventPass {
		t.Fatalf("expected pass, got %+v", cap.msgs)
	}
}

func TestDedupOngoingFailure(t *testing.T) {
	st := testStore(t)
	cfg := testConfig()
	cfg.Notifiers["cap"] = config.Notifier{Type: "webhook", URL: "x", On: []string{"fail"}, RenotifyAfter: config.Duration(24 * time.Hour)}
	d, cap := newDispatcher(t, cfg, st)
	r1 := insertRun(t, st, "01A", store.StatusFail, time.Now().Add(-time.Minute))
	d.Notify(context.Background(), r1, nil)
	r2 := insertRun(t, st, "01B", store.StatusFail, time.Now())
	d.Notify(context.Background(), r2, nil)
	if len(cap.msgs) != 1 {
		t.Fatalf("ongoing failure notified %d times, want 1", len(cap.msgs))
	}
}

func TestRenotifyAfterWindow(t *testing.T) {
	st := testStore(t)
	cfg := testConfig()
	cfg.Notifiers["cap"] = config.Notifier{Type: "webhook", URL: "x", On: []string{"fail"}, RenotifyAfter: config.Duration(time.Millisecond)}
	d, cap := newDispatcher(t, cfg, st)
	r1 := insertRun(t, st, "01A", store.StatusFail, time.Now().Add(-time.Minute))
	d.Notify(context.Background(), r1, nil)
	time.Sleep(5 * time.Millisecond)
	r2 := insertRun(t, st, "01B", store.StatusFail, time.Now())
	d.Notify(context.Background(), r2, nil)
	if len(cap.msgs) != 2 {
		t.Fatalf("expected re-notify after window, got %d", len(cap.msgs))
	}
}

func TestOnFilter(t *testing.T) {
	st := testStore(t)
	cfg := testConfig()
	cfg.Notifiers["cap"] = config.Notifier{Type: "webhook", URL: "x", On: []string{"fail"}}
	d, cap := newDispatcher(t, cfg, st)
	run := insertRun(t, st, "01A", store.StatusPass, time.Now())
	d.Notify(context.Background(), run, nil)
	if len(cap.msgs) != 0 {
		t.Fatalf("pass should not notify an on:[fail] notifier, got %+v", cap.msgs)
	}
}

func TestSkippedDoesNotNotify(t *testing.T) {
	st := testStore(t)
	cfg := testConfig()
	d, cap := newDispatcher(t, cfg, st)
	run := insertRun(t, st, "01A", store.StatusSkipped, time.Now())
	d.Notify(context.Background(), run, nil)
	if len(cap.msgs) != 0 {
		t.Fatalf("skipped must not notify")
	}
}

func TestSecretCanaryNeverInMessage(t *testing.T) {
	st := testStore(t)
	cfg := testConfig()
	canary := "super-secret-canary-12345"
	cfg.Redactor.Add(canary)
	d, cap := newDispatcher(t, cfg, st)
	run := insertRun(t, st, "01A", store.StatusFail, time.Now())
	results := []store.CheckResult{{Name: "http", Status: store.StatusFail, Message: "auth failed with " + canary}}
	d.Notify(context.Background(), run, results)
	if len(cap.msgs) != 1 {
		t.Fatalf("expected one message")
	}
	for _, c := range cap.msgs[0].FailedChecks {
		if strings.Contains(c, canary) {
			t.Fatalf("secret canary leaked into notification: %q", c)
		}
	}
}

func TestWebhookSenderPayloadAndSignature(t *testing.T) {
	var gotBody []byte
	var gotSig string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		gotSig = r.Header.Get("X-Restoredrill-Signature")
		w.WriteHeader(200)
	}))
	defer srv.Close()
	s := &webhookSender{url: srv.URL, secret: "hook-secret", client: srv.Client()}
	m := Message{Drill: "d", RunID: "01A", Status: "fail", Event: EventFail, Timestamp: time.Now(), SnapshotAge: time.Hour}
	if err := s.Send(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	var p webhookPayload
	if err := json.Unmarshal(gotBody, &p); err != nil {
		t.Fatalf("payload not JSON: %v", err)
	}
	if p.Drill != "d" || p.Status != "fail" || p.Event != EventFail {
		t.Fatalf("payload=%+v", p)
	}
	mac := hmac.New(sha256.New, []byte("hook-secret"))
	mac.Write(gotBody)
	want := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	if gotSig != want {
		t.Fatalf("signature=%q want %q", gotSig, want)
	}
}

func TestNtfySender(t *testing.T) {
	var gotTitle, gotAuth, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotTitle = r.Header.Get("Title")
		gotAuth = r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(200)
	}))
	defer srv.Close()
	s := &ntfySender{url: srv.URL, token: "ntfy-token", client: srv.Client()}
	m := Message{Drill: "d", Status: "fail", Event: EventFail}
	if err := s.Send(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(gotTitle, "d") || gotAuth != "Bearer ntfy-token" || !strings.Contains(gotBody, "fail") {
		t.Fatalf("title=%q auth=%q body=%q", gotTitle, gotAuth, gotBody)
	}
}

func TestMatrixSender(t *testing.T) {
	var gotPath, gotAuth string
	var gotPayload map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &gotPayload)
		w.WriteHeader(200)
	}))
	defer srv.Close()
	s := &matrixSender{homeserver: srv.URL, roomID: "!room:example.org", token: "mx-token", client: srv.Client()}
	m := Message{Drill: "d", Status: "pass", Event: EventRecovered, RunID: "01A"}
	if err := s.Send(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(gotPath, "/_matrix/client/v3/rooms/") || !strings.Contains(gotPath, "/send/m.room.message/") {
		t.Fatalf("path=%q", gotPath)
	}
	if gotAuth != "Bearer mx-token" {
		t.Fatalf("auth=%q", gotAuth)
	}
	if gotPayload["msgtype"] != "m.text" || gotPayload["formatted_body"] == nil {
		t.Fatalf("payload=%+v", gotPayload)
	}
}

func TestReadSecretFromFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "token")
	os.WriteFile(p, []byte("file-token\n"), 0o600)
	got, err := readSecret("", p)
	if err != nil || got != "file-token" {
		t.Fatalf("got=%q err=%v", got, err)
	}
}