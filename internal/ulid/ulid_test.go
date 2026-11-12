package ulid

import (
	"sort"
	"testing"
	"time"
)

func TestRoundTrip(t *testing.T) {
	ts := time.Date(2026, 11, 12, 10, 0, 0, 123000000, time.UTC)
	id := NewAt(ts)
	if len(id) != EncodedSize {
		t.Fatalf("length = %d", len(id))
	}
	got, err := Time(id)
	if err != nil {
		t.Fatal(err)
	}
	if got.UnixMilli() != ts.UnixMilli() {
		t.Fatalf("time = %v, want %v", got, ts)
	}
	if !Valid(id) {
		t.Fatalf("Valid(%q) = false", id)
	}
}

func TestMonotonic(t *testing.T) {
	ids := make([]string, 1000)
	for i := range ids {
		ids[i] = New()
	}
	if !sort.StringsAreSorted(ids) {
		t.Fatal("IDs not lexicographically sorted")
	}
	seen := map[string]bool{}
	for _, id := range ids {
		if seen[id] {
			t.Fatalf("duplicate ID %s", id)
		}
		seen[id] = true
	}
}

func TestInvalid(t *testing.T) {
	for _, s := range []string{"", "short", "8ZZZZZZZZZZZZZZZZZZZZZZZZZ", strings26() + "!"} {
		if Valid(s) {
			t.Fatalf("Valid(%q) = true", s)
		}
	}
}

func strings26() string { return "01234567890123456789012345" }