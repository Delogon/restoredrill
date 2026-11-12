// Package redact removes known secret values from every output path: logs,
// stored events, API responses and HTML. The redactor is populated with all
// configured secret values at config load time.
package redact

import (
	"io"
	"strings"
	"sync"
)

// Mask replaces secret occurrences.
const Mask = "[REDACTED]"

// Redactor replaces known secret values with Mask. It is safe for concurrent
// use. Values shorter than 4 bytes are ignored to avoid degenerate
// replacements (an empty secret would otherwise match everywhere).
type Redactor struct {
	mu     sync.RWMutex
	values []string
}

// New returns an empty redactor.
func New() *Redactor { return &Redactor{} }

// Add registers a secret value. Empty and very short values are ignored.
func (r *Redactor) Add(values ...string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, v := range values {
		if len(v) < 4 {
			continue
		}
		// Trim trailing newlines: secret files usually end with one, but the
		// secret may also appear without it in argv/env dumps.
		v = strings.TrimRight(v, "\r\n")
		if len(v) < 4 {
			continue
		}
		dup := false
		for _, existing := range r.values {
			if existing == v {
				dup = true
				break
			}
		}
		if !dup {
			r.values = append(r.values, v)
		}
	}
}

// String returns s with every known secret replaced by Mask.
func (r *Redactor) String(s string) string {
	if r == nil {
		return s
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, v := range r.values {
		s = strings.ReplaceAll(s, v, Mask)
	}
	return s
}

// Bytes is String for byte slices.
func (r *Redactor) Bytes(b []byte) []byte {
	if r == nil || len(b) == 0 {
		return b
	}
	return []byte(r.String(string(b)))
}

// Writer wraps w so that everything written through it is redacted.
// Note: secrets split across Write boundaries are not detected; callers that
// stream untrusted output should buffer lines before writing.
func (r *Redactor) Writer(w io.Writer) io.Writer {
	if r == nil {
		return w
	}
	return &redactWriter{r: r, w: w}
}

type redactWriter struct {
	r *Redactor
	w io.Writer
}

func (rw *redactWriter) Write(p []byte) (int, error) {
	n, err := rw.w.Write(rw.r.Bytes(p))
	if n > len(p) {
		n = len(p)
	}
	return n, err
}