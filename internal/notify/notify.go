// Package notify sends run-result notifications (ntfy, Matrix, webhook) with
// per-event policies and de-duplication of ongoing failures. Content never
// includes secrets or file contents; everything passes through the redactor.
package notify

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/restoredrill/restoredrill/internal/config"
	"github.com/restoredrill/restoredrill/internal/store"
)

// Events.
const (
	EventFail      = "fail"
	EventError     = "error"
	EventRecovered = "recovered"
	EventPass      = "pass"
)

// Message is the redacted, notifier-agnostic content of one notification.
type Message struct {
	Drill        string        `json:"drill"`
	RunID        string        `json:"run_id"`
	Status       string        `json:"status"`
	Event        string        `json:"event"`
	SnapshotAge  time.Duration `json:"-"`
	FailedChecks []string      `json:"failed_checks"`
	ErrorMessage string        `json:"error_message,omitempty"`
	Link         string        `json:"link,omitempty"`
	Timestamp    time.Time     `json:"timestamp"`
}

// Sender delivers a Message to one destination.
type Sender interface {
	Send(ctx context.Context, m Message) error
}

// Dispatcher implements runner.NotifyFunc.
type Dispatcher struct {
	cfg     *config.Config
	store   *store.Store
	log     *slog.Logger
	client  *http.Client
	senders map[string]Sender
}

// New builds a dispatcher. senders may be nil (built from config on demand).
func New(cfg *config.Config, st *store.Store, log *slog.Logger) *Dispatcher {
	return &Dispatcher{
		cfg:    cfg,
		store:  st,
		log:    log,
		client: &http.Client{Timeout: 15 * time.Second},
	}
}

// SetSender overrides a notifier's sender (used by tests).
func (d *Dispatcher) SetSender(name string, s Sender) {
	if d.senders == nil {
		d.senders = map[string]Sender{}
	}
	d.senders[name] = s
}

// Notify is called after a run completes. It matches runner.NotifyFunc.
func (d *Dispatcher) Notify(ctx context.Context, run store.Run, results []store.CheckResult) {
	if run.Status == store.StatusSkipped || run.Status == store.StatusRunning {
		return
	}
	drill, ok := d.cfg.Drills[run.Drill]
	if !ok {
		return
	}
	names := drill.Notify
	if len(names) == 0 {
		names = d.cfg.Defaults.Notify
	}
	if len(names) == 0 {
		return
	}

	event := d.eventFor(run)
	if event == "" {
		return
	}
	msg := d.buildMessage(run, results, event)

	for _, name := range names {
		ncfg, ok := d.cfg.Notifiers[name]
		if !ok {
			d.log.Warn("unknown notifier", "notifier", name, "drill", run.Drill)
			continue
		}
		if !containsEvent(ncfg.On, event) {
			continue
		}
		// De-duplicate ongoing failures within renotify_after.
		if event == EventFail || event == EventError {
			if d.recentlyNotified(run.Drill, name, event, ncfg.RenotifyAfter) {
				continue
			}
		}
		sender, err := d.senderFor(name, ncfg)
		if err != nil {
			d.log.Error("cannot build notifier", "notifier", name, "err", err)
			d.record(run.ID, name, event, false, err.Error())
			continue
		}
		sendErr := sender.Send(ctx, msg)
		if sendErr != nil {
			d.log.Error("notification failed", "notifier", name, "event", event, "err", sendErr)
			d.record(run.ID, name, event, false, sendErr.Error())
		} else {
			d.log.Info("notification sent", "notifier", name, "event", event, "drill", run.Drill)
			d.record(run.ID, name, event, true, "")
		}
	}
}

// eventFor maps a run status to a notification event.
func (d *Dispatcher) eventFor(run store.Run) string {
	switch run.Status {
	case store.StatusFail:
		return EventFail
	case store.StatusError:
		return EventError
	case store.StatusPass:
		prev, err := d.store.LastRunOther(run.Drill, run.ID)
		if err == nil && prev.ID != "" && prev.Status != store.StatusPass {
			return EventRecovered
		}
		return EventPass
	}
	return ""
}

// recentlyNotified reports whether the same event was already sent within the
// renotify window (default 24h).
func (d *Dispatcher) recentlyNotified(drill, name, event string, renotify config.Duration) bool {
	window := renotify.D()
	if window <= 0 {
		window = 24 * time.Hour
	}
	last, err := d.store.LastNotified(drill, name, event)
	if err != nil || last.SentAt.IsZero() || !last.OK {
		return false
	}
	return time.Since(last.SentAt) < window
}

func (d *Dispatcher) buildMessage(run store.Run, results []store.CheckResult, event string) Message {
	m := Message{
		Drill:     run.Drill,
		RunID:     run.ID,
		Status:    run.Status,
		Event:     event,
		Timestamp: run.FinishedAt,
	}
	if !run.SnapshotTime.IsZero() {
		m.SnapshotAge = run.FinishedAt.Sub(run.SnapshotTime)
	}
	for _, r := range results {
		if r.Status == store.StatusFail || r.Status == store.StatusError {
			line := r.Name + ": " + r.Message
			m.FailedChecks = append(m.FailedChecks, d.cfg.Redactor.String(line))
		}
	}
	if run.ErrorMessage != "" {
		m.ErrorMessage = d.cfg.Redactor.String(run.ErrorMessage)
	}
	if d.cfg.Server.PublicURL != "" {
		m.Link = strings.TrimRight(d.cfg.Server.PublicURL, "/") + "/runs/" + run.ID
	}
	return m
}

func (d *Dispatcher) record(runID, name, event string, ok bool, errMsg string) {
	_ = d.store.InsertNotification(store.Notification{
		RunID: runID, Notifier: name, Event: event, SentAt: time.Now(), OK: ok, Error: d.cfg.Redactor.String(errMsg),
	})
}

func (d *Dispatcher) senderFor(name string, n config.Notifier) (Sender, error) {
	if s, ok := d.senders[name]; ok {
		return s, nil
	}
	token, err := readSecret(n.Token, n.TokenFile)
	if err != nil {
		return nil, fmt.Errorf("notifier %s: %w", name, err)
	}
	secret, err := readSecret(n.Secret, n.SecretFile)
	if err != nil {
		return nil, fmt.Errorf("notifier %s: %w", name, err)
	}
	var s Sender
	switch n.Type {
	case "ntfy":
		s = &ntfySender{url: n.URL, token: token, client: d.client}
	case "matrix":
		s = &matrixSender{homeserver: n.Homeserver, roomID: n.RoomID, token: token, client: d.client}
	case "webhook":
		s = &webhookSender{url: n.URL, secret: secret, client: d.client}
	default:
		return nil, fmt.Errorf("notifier %s: unknown type %q", name, n.Type)
	}
	if d.senders == nil {
		d.senders = map[string]Sender{}
	}
	d.senders[name] = s
	return s, nil
}

// readSecret returns the inline value, or reads the file (trimmed).
func readSecret(inline, file string) (string, error) {
	if inline != "" {
		return inline, nil
	}
	if file == "" {
		return "", nil
	}
	b, err := os.ReadFile(file)
	if err != nil {
		return "", fmt.Errorf("cannot read secret file %q: %w", file, err)
	}
	return strings.TrimSpace(string(b)), nil
}

func containsEvent(on []string, event string) bool {
	for _, e := range on {
		if e == event {
			return true
		}
	}
	return false
}