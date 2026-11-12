package checks

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/restoredrill/restoredrill/internal/backend"
	"github.com/restoredrill/restoredrill/internal/config"
	"github.com/restoredrill/restoredrill/internal/store"
)

func TestMatchPath(t *testing.T) {
	cases := []struct {
		pattern, path string
		want          bool
	}{
		{"srv/**/*.pdf", "srv/a/b/c.pdf", true},
		{"srv/**/*.pdf", "srv/c.pdf", true},
		{"srv/**/*.pdf", "srv/c.txt", false},
		{"srv/*.pdf", "srv/a/c.pdf", false},
		{"srv/*.pdf", "srv/c.pdf", true},
		{"**", "anything/at/all", true},
		{"srv/app/data/**", "srv/app/data/f.bin", true},
		{"srv/?.txt", "srv/a.txt", true},
		{"srv/?.txt", "srv/ab.txt", false},
	}
	for _, c := range cases {
		if got := MatchPath(c.pattern, c.path); got != c.want {
			t.Errorf("MatchPath(%q,%q)=%v want %v", c.pattern, c.path, got, c.want)
		}
	}
}

func TestGlobFiles(t *testing.T) {
	root := t.TempDir()
	mk := func(rel string) {
		p := filepath.Join(root, rel)
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte("x"), 0o644)
	}
	mk("srv/a/one.pdf")
	mk("srv/a/b/two.pdf")
	mk("srv/a/b/three.txt")
	files, err := GlobFiles(root, []string{"srv/**/*.pdf"})
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 {
		t.Fatalf("got %d files: %v", len(files), files)
	}
}

func TestJSONPath(t *testing.T) {
	body := `{"version":"1.2.3","users":[{"name":"a"},{"name":"b"}],"count":5}`
	cases := []struct {
		path string
		want string
		ok   bool
	}{
		{"$.version", "1.2.3", true},
		{"$.users[1].name", "b", true},
		{"$.count", "5", true},
		{"$.missing", "", false},
		{"$.users[9].name", "", false},
	}
	for _, c := range cases {
		var doc any
		mustJSON(body, &doc)
		v, ok := jsonPath(doc, c.path)
		if ok != c.ok {
			t.Errorf("path %s ok=%v want %v", c.path, ok, c.ok)
			continue
		}
		if ok && scalarString(v) != c.want {
			t.Errorf("path %s = %q want %q", c.path, scalarString(v), c.want)
		}
	}
}

func mustJSON(s string, v *any) {
	if err := json.Unmarshal([]byte(s), v); err != nil {
		panic(err)
	}
}

func TestSizeTrend(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	prev := store.Run{ID: "PREV", Drill: "d", Status: store.StatusPass, StartedAt: time.Now().Add(-time.Hour), RestoredBytes: 1000, RestoredFiles: 10}
	if err := st.InsertRun(prev); err != nil {
		t.Fatal(err)
	}
	ch, _ := Get("size_trend")
	c := &Context{Store: st, Drill: "d", RunID: "CUR", RestoredBytes: 950, RestoredFiles: 10}
	cfg := config.Check{Type: "size_trend", MaxDropPercent: 10}
	if r := ch.Run(context.Background(), c, cfg); r.Status != Pass {
		t.Fatalf("expected pass for 5%% drop, got %+v", r)
	}
	c.RestoredBytes = 500 // 50% drop
	if r := ch.Run(context.Background(), c, cfg); r.Status != Fail {
		t.Fatalf("expected fail for 50%% drop, got %+v", r)
	}
	// No prior passing run -> skipped.
	c2 := &Context{Store: st, Drill: "other", RunID: "CUR", RestoredBytes: 100, RestoredFiles: 1}
	if r := ch.Run(context.Background(), c2, cfg); r.Status != Skipped {
		t.Fatalf("expected skipped with no baseline, got %+v", r)
	}
}

func TestExpectations(t *testing.T) {
	min := 1.0
	e := config.Expect{Min: &min}
	if ok, _ := matchScalar(e, 5, "5"); !ok {
		t.Fatal("min should pass for 5")
	}
	if ok, _ := matchScalar(e, 0, "0"); ok {
		t.Fatal("min should fail for 0")
	}
	et := config.Expect{Contains: "hello"}
	if ok, _ := matchText(et, "say hello world"); !ok {
		t.Fatal("contains should pass")
	}
	er := config.Expect{Regex: `^v[0-9]+$`}
	if ok, _ := matchText(er, "v123"); !ok {
		t.Fatal("regex should pass")
	}
	exists := true
	je := &config.JSONExpect{Path: "$.version", Exists: &exists}
	if ok, msg := matchJSON(je, `{"version":"1"}`); !ok {
		t.Fatalf("json exists should pass: %s", msg)
	}
	if ok, _ := matchJSON(je, `{"other":"1"}`); ok {
		t.Fatal("json exists should fail when missing")
	}
}

func TestSnapshotAge(t *testing.T) {
	now := time.Now()
	c := &Context{Snapshot: backend.Snapshot{Time: now.Add(-time.Hour)}, Now: now}
	ch, _ := Get("snapshot_age")
	cfg := config.Check{Type: "snapshot_age", MaxAge: config.Duration(2 * time.Hour)}
	if r := ch.Run(context.Background(), c, cfg); r.Status != Pass {
		t.Fatalf("expected pass, got %+v", r)
	}
	cfg.MaxAge = config.Duration(30 * time.Minute)
	if r := ch.Run(context.Background(), c, cfg); r.Status != Fail {
		t.Fatalf("expected fail for old snapshot, got %+v", r)
	}
}

func TestFilesExist(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 5; i++ {
		p := filepath.Join(root, "srv", "data")
		os.MkdirAll(p, 0o755)
		os.WriteFile(filepath.Join(p, string(rune('a'+i))+".pdf"), []byte("content"), 0o644)
	}
	c := &Context{RestoreDir: root}
	ch, _ := Get("files_exist")
	cfg := config.Check{Type: "files_exist", Paths: []string{"srv/data/**/*.pdf"}, MinCount: 5}
	if r := ch.Run(context.Background(), c, cfg); r.Status != Pass {
		t.Fatalf("expected pass, got %+v", r)
	}
	cfg.MinCount = 10
	if r := ch.Run(context.Background(), c, cfg); r.Status != Fail {
		t.Fatalf("expected fail for missing files, got %+v", r)
	}
}

// setupChecksum creates a live tree and an identical restored copy, returning
// the restore dir, source root and snapshot time.
func setupChecksum(t *testing.T) (restoreDir, sourceRoot string, snapTime time.Time) {
	t.Helper()
	base := t.TempDir()
	sourceRoot = filepath.Join(base, "live")
	restoreDir = filepath.Join(base, "restore")
	snapTime = time.Now().Add(-time.Hour)
	rel := "srv/app/data"
	for _, name := range []string{"f1.bin", "f2.bin", "f3.bin", "f4.bin"} {
		content := []byte("content-of-" + name)
		lp := filepath.Join(sourceRoot, rel, name)
		os.MkdirAll(filepath.Dir(lp), 0o755)
		os.WriteFile(lp, content, 0o644)
		os.Chtimes(lp, snapTime.Add(-time.Minute), snapTime.Add(-time.Minute))
		rp := filepath.Join(restoreDir, rel, name)
		os.MkdirAll(filepath.Dir(rp), 0o755)
		os.WriteFile(rp, content, 0o644)
	}
	return restoreDir, sourceRoot, snapTime
}

func TestChecksumSamplePass(t *testing.T) {
	restoreDir, sourceRoot, snapTime := setupChecksum(t)
	c := &Context{RestoreDir: restoreDir, Snapshot: backend.Snapshot{Time: snapTime}}
	ch, _ := Get("checksum_sample")
	cfg := config.Check{Type: "checksum_sample", SourceRoot: sourceRoot, Paths: []string{"srv/app/data/**"}, Sample: 4, Seed: 1}
	if r := ch.Run(context.Background(), c, cfg); r.Status != Pass {
		t.Fatalf("expected pass, got %+v", r)
	}
}

func TestChecksumSampleCorruptFails(t *testing.T) {
	restoreDir, sourceRoot, snapTime := setupChecksum(t)
	// Corrupt one restored file (same size, different content).
	p := filepath.Join(restoreDir, "srv/app/data/f1.bin")
	os.WriteFile(p, []byte("CORRUPTED-XXXXXXX"), 0o644) // 17 bytes, same as original
	c := &Context{RestoreDir: restoreDir, Snapshot: backend.Snapshot{Time: snapTime}}
	ch, _ := Get("checksum_sample")
	cfg := config.Check{Type: "checksum_sample", SourceRoot: sourceRoot, Paths: []string{"srv/app/data/**"}, Sample: 4, Seed: 1}
	if r := ch.Run(context.Background(), c, cfg); r.Status != Fail {
		t.Fatalf("expected fail for corrupted file, got %+v", r)
	}
}

func TestChecksumSampleChangedAfterBackupSkipped(t *testing.T) {
	restoreDir, sourceRoot, snapTime := setupChecksum(t)
	// Modify live files AFTER the snapshot: they must be skipped, not failed.
	for _, name := range []string{"f1.bin", "f2.bin", "f3.bin"} {
		p := filepath.Join(sourceRoot, "srv/app/data", name)
		os.WriteFile(p, []byte("changed-after-backup"), 0o644)
		os.Chtimes(p, snapTime.Add(time.Hour), snapTime.Add(time.Hour))
	}
	c := &Context{RestoreDir: restoreDir, Snapshot: backend.Snapshot{Time: snapTime}}
	ch, _ := Get("checksum_sample")
	// Only 1 of 4 qualifies (< half) -> error.
	cfg := config.Check{Type: "checksum_sample", SourceRoot: sourceRoot, Paths: []string{"srv/app/data/**"}, Sample: 4, Seed: 1}
	if r := ch.Run(context.Background(), c, cfg); r.Status != Error {
		t.Fatalf("expected error when <half qualify, got %+v", r)
	}
}