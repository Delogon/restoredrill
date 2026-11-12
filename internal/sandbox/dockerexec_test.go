package sandbox

import (
	"archive/tar"
	"bytes"
	"encoding/binary"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func frame(typ byte, payload string) []byte {
	b := make([]byte, 8+len(payload))
	b[0] = typ
	binary.BigEndian.PutUint32(b[4:8], uint32(len(payload)))
	copy(b[8:], payload)
	return b
}

func TestDemuxStreamSeparatesStdoutStderr(t *testing.T) {
	var buf bytes.Buffer
	buf.Write(frame(1, "out1"))
	buf.Write(frame(2, "err1"))
	buf.Write(frame(1, "out2"))
	stdout, stderr, err := demuxStream(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if stdout != "out1out2" || stderr != "err1" {
		t.Fatalf("stdout=%q stderr=%q", stdout, stderr)
	}
}

func TestDemuxStreamEmpty(t *testing.T) {
	stdout, stderr, err := demuxStream(bytes.NewReader(nil))
	if err != nil || stdout != "" || stderr != "" {
		t.Fatalf("got %q %q %v", stdout, stderr, err)
	}
}

func TestDemuxStreamNonMultiplexed(t *testing.T) {
	// A TTY stream is raw bytes, not framed. It must not be lost.
	stdout, _, err := demuxStream(bytes.NewReader([]byte("plain output")))
	if err != nil {
		t.Fatal(err)
	}
	if stdout != "plain output" {
		t.Fatalf("stdout=%q", stdout)
	}
}

func TestMakeTarFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "db.sqlite3")
	os.WriteFile(p, []byte("data"), 0o644)
	b, err := makeTar(p, "db.sqlite3")
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(bytes.NewReader(b))
	hdr, err := tr.Next()
	if err != nil {
		t.Fatal(err)
	}
	if hdr.Name != "db.sqlite3" {
		t.Fatalf("name=%q", hdr.Name)
	}
	content, _ := io.ReadAll(tr)
	if string(content) != "data" {
		t.Fatalf("content=%q", content)
	}
}

func TestMakeTarDir(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "sub"), 0o755)
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a"), 0o644)
	os.WriteFile(filepath.Join(dir, "sub", "b.txt"), []byte("b"), 0o644)
	b, err := makeTar(dir, "root")
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	tr := tar.NewReader(bytes.NewReader(b))
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		names[hdr.Name] = true
	}
	for _, want := range []string{"root/", "root/a.txt", "root/sub/", "root/sub/b.txt"} {
		if !names[want] {
			t.Fatalf("missing %q in tar (got %v)", want, names)
		}
	}
}