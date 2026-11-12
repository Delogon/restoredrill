package sandbox

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
)

// DockerClient is a minimal Docker Engine API client over the Docker-compatible
// socket (Docker or rootless Podman). It implements only the read/create/delete
// operations restoredrill needs; it never touches repositories.
type DockerClient struct {
	http *http.Client
	host string // scheme://host for request URLs (unix socket is the transport)
}

// NewDockerClient connects to the Docker socket. It honours DOCKER_HOST
// (unix:// or tcp://) and falls back to /var/run/docker.sock.
func NewDockerClient() (*DockerClient, error) {
	host := os.Getenv("DOCKER_HOST")
	if host == "" {
		host = "unix:///var/run/docker.sock"
	}
	u, err := url.Parse(host)
	if err != nil {
		return nil, fmt.Errorf("invalid DOCKER_HOST %q: %w", host, err)
	}
	var dial func(ctx context.Context, network, addr string) (net.Conn, error)
	var schemeHost string
	switch u.Scheme {
	case "unix":
		sock := u.Path
		dial = func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", sock)
		}
		schemeHost = "http://docker"
	case "tcp", "http", "https":
		addr := u.Host
		dial = func(ctx context.Context, network, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, network, addr)
		}
		schemeHost = "http://" + addr
	default:
		return nil, fmt.Errorf("unsupported DOCKER_HOST scheme %q", u.Scheme)
	}
	return &DockerClient{
		http: &http.Client{Transport: &http.Transport{DialContext: dial}},
		host: schemeHost,
	}, nil
}

func (c *DockerClient) do(ctx context.Context, method, path string, body any, out any) error {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.host+path, rdr)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("cannot reach the Docker socket — is Docker/Podman running? (%w)", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode >= 400 {
		return fmt.Errorf("docker API %s %s: %s: %s", method, path, resp.Status, strings.TrimSpace(string(data)))
	}
	if out != nil && len(data) > 0 {
		if err := json.Unmarshal(data, out); err != nil {
			return fmt.Errorf("cannot decode docker API response: %w", err)
		}
	}
	return nil
}

// Ping checks that the daemon is reachable.
func (c *DockerClient) Ping(ctx context.Context) error {
	return c.do(ctx, http.MethodGet, "/_ping", nil, nil)
}

// ImageExists reports whether the image is present locally.
func (c *DockerClient) ImageExists(ctx context.Context, image string) bool {
	var out []struct {
		ID string `json:"Id"`
	}
	err := c.do(ctx, http.MethodGet, "/images/json?reference="+url.QueryEscape(image), nil, &out)
	return err == nil && len(out) > 0
}

// PullImage pulls an image, streaming progress to the caller (may be nil).
func (c *DockerClient) PullImage(ctx context.Context, image string) error {
	ref := image
	tag := "latest"
	if i := strings.LastIndex(image, ":"); i > strings.LastIndex(image, "/") {
		ref = image[:i]
		tag = image[i+1:]
	}
	path := "/images/create?fromImage=" + url.QueryEscape(ref) + "&tag=" + url.QueryEscape(tag)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.host+path, nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		data, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("cannot pull image %s: %s", image, strings.TrimSpace(string(data)))
	}
	// Drain the progress stream; surface any error message.
	dec := json.NewDecoder(resp.Body)
	for {
		var msg struct {
			Error string `json:"error"`
		}
		if err := dec.Decode(&msg); err != nil {
			break
		}
		if msg.Error != "" {
			return fmt.Errorf("cannot pull image %s: %s", image, msg.Error)
		}
	}
	return nil
}

// EnsureImage pulls the image if it is not present locally.
func (c *DockerClient) EnsureImage(ctx context.Context, image string) error {
	if c.ImageExists(ctx, image) {
		return nil
	}
	return c.PullImage(ctx, image)
}

// BuildImage builds an image from a tar build context (Dockerfile + files) and
// tags it. It streams the build log and surfaces any error.
func (c *DockerClient) BuildImage(ctx context.Context, tarContext []byte, tag string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.host+"/build?t="+url.QueryEscape(tag), bytes.NewReader(tarContext))
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
		return fmt.Errorf("cannot build image %s: %s", tag, strings.TrimSpace(string(data)))
	}
	dec := json.NewDecoder(resp.Body)
	for {
		var msg struct {
			Error string `json:"error"`
			Stream string `json:"stream"`
		}
		if err := dec.Decode(&msg); err != nil {
			break
		}
		if msg.Error != "" {
			return fmt.Errorf("cannot build image %s: %s", tag, msg.Error)
		}
	}
	return nil
}

// NetworkCreate creates an internal (no-egress) network and returns its ID.
func (c *DockerClient) NetworkCreate(ctx context.Context, name string, labels map[string]string) (string, error) {
	body := map[string]any{
		"Name":       name,
		"Internal":   true, // no egress, no published ports
		"Driver":     "bridge",
		"Labels":     labels,
		"Attachable": false,
	}
	var out struct {
		ID string `json:"Id"`
	}
	if err := c.do(ctx, http.MethodPost, "/networks/create", body, &out); err != nil {
		return "", err
	}
	return out.ID, nil
}

// NetworkRemove removes a network by ID or name.
func (c *DockerClient) NetworkRemove(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodDelete, "/networks/"+id, nil, nil)
}

// RemoveImage removes a local image by tag or ID (best-effort cleanup).
func (c *DockerClient) RemoveImage(ctx context.Context, image string) error {
	return c.do(ctx, http.MethodDelete, "/images/"+url.PathEscape(image), nil, nil)
}

// ContainerConfig describes a container to create.
type ContainerConfig struct {
	Image        string
	Name         string
	Cmd          []string
	Env          map[string]string
	Mounts       []Mount
	Labels       map[string]string
	Network      string
	NetworkAlias string
	CPUs         float64
	MemoryBytes  int64
	CapAdd       []string
	User         string
}

// ContainerCreate creates a container with hardened defaults and returns its ID.
func (c *DockerClient) ContainerCreate(ctx context.Context, cfg ContainerConfig) (string, error) {
	env := make([]string, 0, len(cfg.Env))
	for k, v := range cfg.Env {
		env = append(env, k+"="+v)
	}
	var binds []string
	for _, m := range cfg.Mounts {
		b := m.Source + ":" + m.Target
		if m.ReadOnly {
			b += ":ro"
		}
		binds = append(binds, b)
	}
	hostCfg := map[string]any{
		"Binds":       binds,
		"NetworkMode": cfg.Network,
		"SecurityOpt": []string{"no-new-privileges:true"},
		"CapDrop":     []string{"ALL"},
		"ReadonlyRootfs": false,
	}
	if cfg.CPUs > 0 {
		hostCfg["NanoCpus"] = int64(cfg.CPUs * 1e9)
	}
	if cfg.MemoryBytes > 0 {
		hostCfg["Memory"] = cfg.MemoryBytes
	}
	if len(cfg.CapAdd) > 0 {
		hostCfg["CapAdd"] = cfg.CapAdd
	}
	networking := map[string]any{}
	if cfg.NetworkAlias != "" {
		networking["EndpointsConfig"] = map[string]any{
			cfg.Network: map[string]any{"Aliases": []string{cfg.NetworkAlias}},
		}
	}
	body := map[string]any{
		"Image":        cfg.Image,
		"Cmd":          cfg.Cmd,
		"Env":          env,
		"Labels":       cfg.Labels,
		"HostConfig":   hostCfg,
		"NetworkingConfig": networking,
	}
	if cfg.User != "" {
		body["User"] = cfg.User
	}
	var out struct {
		ID string `json:"Id"`
	}
	if err := c.do(ctx, http.MethodPost, "/containers/create?name="+url.QueryEscape(cfg.Name), body, &out); err != nil {
		return "", err
	}
	return out.ID, nil
}

// ContainerStart starts a container.
func (c *DockerClient) ContainerStart(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodPost, "/containers/"+id+"/start", nil, nil)
}

// ContainerStop stops a container (with a short grace period).
func (c *DockerClient) ContainerStop(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodPost, "/containers/"+id+"/stop?t=3", nil, nil)
}

// ContainerRemove force-removes a container.
func (c *DockerClient) ContainerRemove(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodDelete, "/containers/"+id+"?force=1&v=1", nil, nil)
}

// ContainerState is the subset of inspect output we need.
type ContainerState struct {
	ID    string
	State struct {
		Status  string `json:"Status"`
		Running bool   `json:"Running"`
		Health  struct {
			Status string `json:"Status"`
		} `json:"Health"`
		ExitCode int `json:"ExitCode"`
	}
}

// ContainerInspect inspects a container.
func (c *DockerClient) ContainerInspect(ctx context.Context, id string) (*ContainerState, error) {
	var raw struct {
		ID    string `json:"Id"`
		State struct {
			Status  string `json:"Status"`
			Running bool   `json:"Running"`
			Health  struct {
				Status string `json:"Status"`
			} `json:"Health"`
			ExitCode int `json:"ExitCode"`
		} `json:"State"`
	}
	if err := c.do(ctx, http.MethodGet, "/containers/"+id+"/json", nil, &raw); err != nil {
		return nil, err
	}
	cs := &ContainerState{ID: raw.ID}
	cs.State.Status = raw.State.Status
	cs.State.Running = raw.State.Running
	cs.State.Health.Status = raw.State.Health.Status
	cs.State.ExitCode = raw.State.ExitCode
	return cs, nil
}

// ContainerSummary is one entry from the container list.
type ContainerSummary struct {
	ID     string            `json:"Id"`
	Names  []string          `json:"Names"`
	Labels map[string]string `json:"Labels"`
	State  string            `json:"State"`
}

// ContainersByLabel lists containers carrying a label key=value.
func (c *DockerClient) ContainersByLabel(ctx context.Context, key, value string) ([]ContainerSummary, error) {
	filters := fmt.Sprintf(`{"label":[%q]}`, key+"="+value)
	var out []ContainerSummary
	err := c.do(ctx, http.MethodGet, "/containers/json?all=1&filters="+url.QueryEscape(filters), nil, &out)
	return out, err
}

// AllManagedContainers lists every container with the managed label.
func (c *DockerClient) AllManagedContainers(ctx context.Context) ([]ContainerSummary, error) {
	filters := fmt.Sprintf(`{"label":[%q]}`, LabelManaged+"=true")
	var out []ContainerSummary
	err := c.do(ctx, http.MethodGet, "/containers/json?all=1&filters="+url.QueryEscape(filters), nil, &out)
	return out, err
}

// NetworkSummary is one entry from the network list.
type NetworkSummary struct {
	ID     string            `json:"Id"`
	Name   string            `json:"Name"`
	Labels map[string]string `json:"Labels"`
}

// AllManagedNetworks lists every network with the managed label.
func (c *DockerClient) AllManagedNetworks(ctx context.Context) ([]NetworkSummary, error) {
	filters := fmt.Sprintf(`{"label":[%q]}`, LabelManaged+"=true")
	var out []NetworkSummary
	err := c.do(ctx, http.MethodGet, "/networks?filters="+url.QueryEscape(filters), nil, &out)
	return out, err
}

// LabelManaged marks containers/networks created by restoredrill.
const LabelManaged = "restoredrill.managed"