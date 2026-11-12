package notify

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// webhookSender POSTs a documented JSON payload to a URL, optionally signed
// with HMAC-SHA256 (X-Restoredrill-Signature: sha256=<hex>).
type webhookSender struct {
	url    string
	secret string
	client *http.Client
}

// webhookPayload is the stable, documented webhook body.
type webhookPayload struct {
	Drill           string   `json:"drill"`
	RunID           string   `json:"run_id"`
	Status          string   `json:"status"`
	Event           string   `json:"event"`
	SnapshotAgeSecs float64  `json:"snapshot_age_seconds"`
	FailedChecks    []string `json:"failed_checks"`
	ErrorMessage    string   `json:"error_message,omitempty"`
	Link            string   `json:"link,omitempty"`
	Timestamp       string   `json:"timestamp"`
}

func (s *webhookSender) Send(ctx context.Context, m Message) error {
	p := webhookPayload{
		Drill:           m.Drill,
		RunID:           m.RunID,
		Status:          m.Status,
		Event:           m.Event,
		SnapshotAgeSecs: m.SnapshotAge.Seconds(),
		FailedChecks:    m.FailedChecks,
		ErrorMessage:    m.ErrorMessage,
		Link:            m.Link,
		Timestamp:       m.Timestamp.UTC().Format("2006-01-02T15:04:05Z"),
	}
	if p.FailedChecks == nil {
		p.FailedChecks = []string{}
	}
	b, err := json.Marshal(p)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.url, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if s.secret != "" {
		mac := hmac.New(sha256.New, []byte(s.secret))
		mac.Write(b)
		req.Header.Set("X-Restoredrill-Signature", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("webhook returned %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	return nil
}
