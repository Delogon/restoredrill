package redact

import (
	"bytes"
	"strings"
	"testing"
)

func TestRedaction(t *testing.T) {
	r := New()
	r.Add("canary-secret-value", "short") // "short" is >=4, kept
	if got := r.String("password=canary-secret-value end"); got != "password=[REDACTED] end" {
		t.Fatalf("got %q", got)
	}
	// Trailing newline variants.
	r.Add("token-with-newline\n")
	if got := r.String("a token-with-newline b"); !strings.Contains(got, Mask) {
		t.Fatalf("got %q", got)
	}
	// Short values ignored.
	r.Add("ab")
	if got := r.String("ab"); got != "ab" {
		t.Fatalf("short value should be ignored, got %q", got)
	}
}

func TestWriter(t *testing.T) {
	r := New()
	r.Add("supersecret")
	var buf bytes.Buffer
	w := r.Writer(&buf)
	w.Write([]byte("before supersecret after"))
	if strings.Contains(buf.String(), "supersecret") {
		t.Fatalf("writer leaked secret: %q", buf.String())
	}
}

func TestNilRedactor(t *testing.T) {
	var r *Redactor
	if got := r.String("anything"); got != "anything" {
		t.Fatalf("nil redactor changed string: %q", got)
	}
}