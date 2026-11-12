package web

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"math"
	"net/http"
	"strings"
	"time"
)

// parseTemplates loads the layout plus each page as its own template set.
func (s *Server) parseTemplates() error {
	s.pages = map[string]*template.Template{}
	for _, p := range []string{"overview", "drill", "run", "live", "error"} {
		t, err := template.New("layout").Funcs(s.funcMap()).ParseFS(embeddedFS,
			"templates/layout.html", "templates/"+p+".html", "templates/partials/*.html")
		if err != nil {
			return fmt.Errorf("parse template %s: %w", p, err)
		}
		s.pages[p] = t
	}
	t, err := template.New("partials").Funcs(s.funcMap()).ParseFS(embeddedFS, "templates/partials/*.html")
	if err != nil {
		return fmt.Errorf("parse partials: %w", err)
	}
	s.partials = t
	return nil
}

func (s *Server) funcMap() template.FuncMap {
	return template.FuncMap{
		"icon":       statusIcon,
		"fmtTime":    func(t time.Time) string { return s.fmtTime(t) },
		"ago":        s.ago,
		"ageOf":      s.ageOf,
		"durMS":      func(ms int64) string { return fmtDuration(time.Duration(ms) * time.Millisecond) },
		"durSecs":    func(sec float64) string { return fmtDuration(time.Duration(sec * float64(time.Second))) },
		"between":    func(a, b time.Time) string { return fmtDuration(a.Sub(b)) },
		"fmtBytes":   humanBytes,
		"spark":      sparkline,
		"prettyJSON": prettyJSON,
	}
}

func (s *Server) fmtTime(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	return t.In(s.loc).Format("2006-01-02 15:04:05")
}

// render executes a page template into a buffer first so a template error
// produces a clean 500 instead of a half-written page. The whole page passes
// through the redactor before it leaves the process (defense in depth: stored
// data is already redacted at persistence).
func (s *Server) render(w http.ResponseWriter, status int, page string, data any) {
	t, ok := s.pages[page]
	if !ok {
		http.Error(w, "unknown page", http.StatusInternalServerError)
		return
	}
	var buf bytes.Buffer
	if err := t.ExecuteTemplate(&buf, "layout", data); err != nil {
		s.log.Error("template render failed", "page", page, "err", err)
		http.Error(w, "internal error rendering page", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, s.cfg.Redactor.String(buf.String()))
}

func (s *Server) renderError(w http.ResponseWriter, status int, msg string) {
	s.render(w, status, "error", map[string]any{"Message": msg, "Status": status})
}

// statusIcon returns a text glyph for a status. Status is always shown with
// text and icon, never colour alone.
func statusIcon(status string) string {
	switch status {
	case "pass":
		return "✔"
	case "fail":
		return "✘"
	case "error":
		return "⚠"
	case "running":
		return "⟳"
	case "skipped":
		return "⏭"
	default:
		return "·"
	}
}

// ago renders a coarse relative time ("3m ago"); zero means "never".
func (s *Server) ago(t time.Time) string {
	if t.IsZero() {
		return "never"
	}
	d := s.now().Sub(t)
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

// ageOf renders the age of a snapshot timestamp.
func (s *Server) ageOf(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	return fmtDuration(s.now().Sub(t))
}

func fmtDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Second:
		return fmt.Sprintf("%dms", d.Milliseconds())
	case d < time.Minute:
		return fmt.Sprintf("%.1fs", d.Seconds())
	case d < time.Hour:
		return fmt.Sprintf("%dm %02ds", int(d.Minutes()), int(d.Seconds())%60)
	default:
		return fmt.Sprintf("%dh %02dm", int(d.Hours()), int(d.Minutes())%60)
	}
}

func humanBytes(n int64) string {
	if n < 1024 {
		return fmt.Sprintf("%d B", n)
	}
	units := []string{"KiB", "MiB", "GiB", "TiB", "PiB"}
	v := float64(n)
	i := -1
	for v >= 1024 && i < len(units)-1 {
		v /= 1024
		i++
	}
	return fmt.Sprintf("%.1f %s", v, units[i])
}

// sparkline renders a tiny inline SVG trend (oldest value first). It is plain
// markup: no scripts, works without JavaScript and under the strict CSP.
func sparkline(values []float64) template.HTML {
	if len(values) < 2 {
		return template.HTML(`<span class="muted">—</span>`)
	}
	const w, h = 120.0, 32.0
	min, max := values[0], values[0]
	for _, v := range values {
		min = math.Min(min, v)
		max = math.Max(max, v)
	}
	span := max - min
	if span == 0 {
		span = 1
	}
	var pts []string
	for i, v := range values {
		x := float64(i) / float64(len(values)-1) * w
		y := h - 2 - (v-min)/span*(h-4)
		pts = append(pts, fmt.Sprintf("%.1f,%.1f", x, y))
	}
	return template.HTML(fmt.Sprintf(
		`<svg class="spark" viewBox="0 0 %.0f %.0f" width="%.0f" height="%.0f" role="img" aria-label="trend"><polyline points="%s"/></svg>`,
		w, h, w, h, strings.Join(pts, " ")))
}

// prettyJSON indents a JSON string for display; invalid JSON is returned as-is.
func prettyJSON(s string) string {
	if s == "" {
		return ""
	}
	var buf bytes.Buffer
	if err := json.Indent(&buf, []byte(s), "", "  "); err != nil {
		return s
	}
	return buf.String()
}