package notify

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// ntfySender publishes to an ntfy topic (https://ntfy.sh or self-hosted).
type ntfySender struct {
	url    string
	token  string
	client *http.Client
}

func (s *ntfySender) Send(ctx context.Context, m Message) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.url, strings.NewReader(plainText(m)))
	if err != nil {
		return err
	}
	req.Header.Set("Title", fmt.Sprintf("restoredrill: %s %s", m.Drill, m.Status))
	req.Header.Set("Tags", tagsFor(m.Status))
	if m.Status == "fail" || m.Status == "error" {
		req.Header.Set("Priority", "high")
	}
	if s.token != "" {
		req.Header.Set("Authorization", "Bearer "+s.token)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("ntfy returned %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	return nil
}

func tagsFor(status string) string {
	switch status {
	case "pass":
		return "white_check_mark"
	case "fail":
		return "x"
	case "error":
		return "warning"
	default:
		return "floppy_disk"
	}
}