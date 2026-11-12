package sandbox

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/restoredrill/restoredrill/internal/ulid"
)

func TestGCRemovesInactiveDirsKeepsActiveAndKept(t *testing.T) {
	work := t.TempDir()
	activeID := ulid.New()
	inactiveID := ulid.New()
	keptID := ulid.New()
	expiredID := ulid.New()
	for _, id := range []string{activeID, inactiveID, keptID, expiredID} {
		os.MkdirAll(filepath.Join(work, id, "restore"), 0o700)
	}
	// A non-run directory and a plain file must be left alone.
	os.MkdirAll(filepath.Join(work, "not-a-run-id"), 0o755)
	os.WriteFile(filepath.Join(work, "stray-file"), []byte("x"), 0o644)

	shouldKeep := func(id string) bool { return id == keptID }
	n, err := GC(context.Background(), nil, []string{activeID}, work, shouldKeep)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 { // inactiveID + expiredID
		t.Fatalf("removed=%d, want 2", n)
	}
	for _, id := range []string{activeID, keptID} {
		if _, err := os.Stat(filepath.Join(work, id)); err != nil {
			t.Fatalf("kept/active dir %s was removed", id)
		}
	}
	for _, id := range []string{inactiveID, expiredID} {
		if _, err := os.Stat(filepath.Join(work, id)); !os.IsNotExist(err) {
			t.Fatalf("inactive dir %s was not removed", id)
		}
	}
	if _, err := os.Stat(filepath.Join(work, "not-a-run-id")); err != nil {
		t.Fatal("non-run directory was removed")
	}
	if _, err := os.Stat(filepath.Join(work, "stray-file")); err != nil {
		t.Fatal("stray file was removed")
	}
}

func TestGCMissingWorkDirIsOK(t *testing.T) {
	n, err := GC(context.Background(), nil, nil, filepath.Join(t.TempDir(), "does-not-exist"), nil)
	if err != nil || n != 0 {
		t.Fatalf("n=%d err=%v", n, err)
	}
}