package sandbox

import (
	"archive/tar"
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// Exec runs a command inside a running container and returns its exit code,
// stdout and stderr.
func (c *DockerClient) Exec(ctx context.Context, container string, cmd []string) (int, string, string, error) {
	var created struct {
		ID string `json:"Id"`
	}
	body := map[string]any{
		"AttachStdout": true,
		"AttachStderr": true,
		"Cmd":          cmd,
	}
	if err := c.do(ctx, http.MethodPost, "/containers/"+container+"/exec", body, &created); err != nil {
		return -1, "", "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.host+"/exec/"+created.ID+"/start", bytes.NewReader([]byte(`{"Detach":false,"Tty":false}`)))
	if err != nil {
		return -1, "", "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return -1, "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		data, _ := io.ReadAll(resp.Body)
		return -1, "", "", fmt.Errorf("exec start: %s: %s", resp.Status, strings.TrimSpace(string(data)))
	}
	stdout, stderr, err := demuxStream(resp.Body)
	if err != nil {
		return -1, "", "", err
	}
	var inspect struct {
		ExitCode int `json:"ExitCode"`
	}
	if err := c.do(ctx, http.MethodGet, "/exec/"+created.ID+"/json", nil, &inspect); err != nil {
		return -1, stdout, stderr, err
	}
	return inspect.ExitCode, stdout, stderr, nil
}

// demuxStream separates Docker's multiplexed stdout/stderr stream. Each frame
// has an 8-byte header: [type(1), 0,0,0, size(4 big-endian)] then payload.
// type 1 = stdout, 2 = stderr. If the stream is not multiplexed (TTY), it is
// returned verbatim as stdout.
func demuxStream(r io.Reader) (stdout, stderr string, err error) {
	br := bufio.NewReader(r)
	var out, errb bytes.Buffer
	// Decide multiplexed vs raw by peeking at the first header without
	// consuming it, so raw (TTY) output is not misread as a frame.
	peek, _ := br.Peek(8)
	if !validFrameHeader(peek) {
		_, _ = io.Copy(&out, br)
		return out.String(), "", nil
	}
	header := make([]byte, 8)
	for {
		_, err := io.ReadFull(br, header)
		if err == io.EOF {
			break
		}
		if err != nil {
			return out.String(), errb.String(), nil
		}
		size := binary.BigEndian.Uint32(header[4:8])
		if size > 64*1024*1024 {
			return out.String(), errb.String(), fmt.Errorf("exec frame too large")
		}
		payload := make([]byte, size)
		if _, err := io.ReadFull(br, payload); err != nil {
			return out.String(), errb.String(), nil
		}
		switch header[0] {
		case 2:
			errb.Write(payload)
		default:
			out.Write(payload)
		}
	}
	return out.String(), errb.String(), nil
}

// validFrameHeader reports whether b looks like a Docker stream frame header:
// stream type 0/1/2 followed by three zero bytes.
func validFrameHeader(b []byte) bool {
	if len(b) < 8 || b[0] > 2 {
		return false
	}
	return b[1] == 0 && b[2] == 0 && b[3] == 0
}

// CopyToContainer copies a local file or directory into the container at the
// given destination path (Docker archive API, tar upload).
func (c *DockerClient) CopyToContainer(ctx context.Context, container, localPath, containerPath string) error {
	tarball, err := makeTar(localPath, filepath.Base(containerPath))
	if err != nil {
		return err
	}
	dest := filepath.Dir(containerPath)
	req, err := http.NewRequestWithContext(ctx, http.MethodPut,
		c.host+"/containers/"+container+"/archive?path="+url.QueryEscape(dest), bytes.NewReader(tarball))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-tar")
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		data, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("copy to container: %s: %s", resp.Status, strings.TrimSpace(string(data)))
	}
	return nil
}

// makeTar builds a tar archive of a file or directory, rooted at name.
func makeTar(localPath, name string) ([]byte, error) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	fi, err := os.Stat(localPath)
	if err != nil {
		return nil, err
	}
	if fi.IsDir() {
		err = filepath.Walk(localPath, func(p string, d os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(localPath, p)
			if err != nil {
				return err
			}
			hdr, err := tar.FileInfoHeader(d, "")
			if err != nil {
				return err
			}
			hdr.Name = filepath.ToSlash(filepath.Join(name, rel))
			if d.IsDir() {
				hdr.Name += "/"
			}
			if err := tw.WriteHeader(hdr); err != nil {
				return err
			}
			if d.Mode().IsRegular() {
				f, err := os.Open(p)
				if err != nil {
					return err
				}
				defer f.Close()
				if _, err := io.Copy(tw, f); err != nil {
					return err
				}
			}
			return nil
		})
	} else {
		var hdr *tar.Header
		hdr, err = tar.FileInfoHeader(fi, "")
		if err != nil {
			return nil, err
		}
		hdr.Name = name
		if err = tw.WriteHeader(hdr); err != nil {
			return nil, err
		}
		var f *os.File
		f, err = os.Open(localPath)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		_, err = io.Copy(tw, f)
	}
	if err != nil {
		return nil, err
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// ContainerLogs returns the combined logs of a container (for debugging).
func (c *DockerClient) ContainerLogs(ctx context.Context, container string, tail int) (string, error) {
	path := fmt.Sprintf("/containers/%s/logs?stdout=1&stderr=1&tail=%d", container, tail)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.host+path, nil)
	if err != nil {
		return "", err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	stdout, stderr, _ := demuxStream(resp.Body)
	return stdout + stderr, nil
}