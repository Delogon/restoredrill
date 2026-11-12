package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// matrixSender posts to a Matrix room via the client-server API.
type matrixSender struct {
	homeserver string
	roomID     string
	token      string
	client     *http.Client
}

func (s *matrixSender) Send(ctx context.Context, m Message) error {
	txn := fmt.Sprintf("restoredrill-%s-%s-%d", m.RunID, m.Event, time.Now().UnixNano())
	endpoint := strings.TrimRight(s.homeserver, "/") +
		"/_matrix/client/v3/rooms/" + url.PathEscape(s.roomID) +
		"/send/m.room.message/" + url.PathEscape(txn)
	payload := map[string]any{
		"msgtype":        "m.text",
		"body":           plainText(m),
		"format":         "org.matrix.custom.html",
		"formatted_body": htmlBody(m),
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, endpoint, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
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
		return fmt.Errorf("matrix returned %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	return nil
}