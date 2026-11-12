package notify

import (
	"fmt"
	"strings"
	"time"
)

// plainText renders a Message as a short human-readable notification body.
func plainText(m Message) string {
	var sb strings.Builder
	icon := statusIcon(m.Status)
	fmt.Fprintf(&sb, "%s %s: %s\n", icon, m.Drill, m.Status)
	if m.Event == EventRecovered {
		sb.WriteString("Recovered: the first passing run after a failure.\n")
	}
	if m.ErrorMessage != "" {
		fmt.Fprintf(&sb, "Error: %s\n", m.ErrorMessage)
	}
	for _, c := range m.FailedChecks {
		fmt.Fprintf(&sb, "  ✘ %s\n", c)
	}
	if m.SnapshotAge > 0 {
		fmt.Fprintf(&sb, "Snapshot age: %s\n", m.SnapshotAge.Round(time.Minute))
	}
	if m.Link != "" {
		fmt.Fprintf(&sb, "%s\n", m.Link)
	}
	return strings.TrimRight(sb.String(), "\n")
}

// htmlBody renders a Message as simple HTML (Matrix formatted_body).
func htmlBody(m Message) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "<strong>%s %s: %s</strong>", statusIcon(m.Status), escapeHTML(m.Drill), escapeHTML(m.Status))
	if m.Event == EventRecovered {
		sb.WriteString("<br>Recovered: the first passing run after a failure.")
	}
	if m.ErrorMessage != "" {
		fmt.Fprintf(&sb, "<br>Error: %s", escapeHTML(m.ErrorMessage))
	}
	for _, c := range m.FailedChecks {
		fmt.Fprintf(&sb, "<br>✘ %s", escapeHTML(c))
	}
	if m.SnapshotAge > 0 {
		fmt.Fprintf(&sb, "<br>Snapshot age: %s", m.SnapshotAge.Round(time.Minute))
	}
	if m.Link != "" {
		fmt.Fprintf(&sb, `<br><a href="%s">%s</a>`, escapeHTML(m.Link), escapeHTML(m.Link))
	}
	return sb.String()
}

func statusIcon(status string) string {
	switch status {
	case "pass":
		return "✔"
	case "fail":
		return "✘"
	case "error":
		return "!"
	default:
		return "•"
	}
}

func escapeHTML(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&#34;", "'", "&#39;")
	return r.Replace(s)
}