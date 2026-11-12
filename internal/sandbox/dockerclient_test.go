package sandbox

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/restoredrill/restoredrill/internal/config"
)

func serviceCfg() config.Service {
	return config.Service{Image: "busybox"}
}

// fakeDocker is an in-memory Docker Engine API used to verify the requests the
// runtime makes (hardening, labels, teardown, GC) without a real daemon.
type fakeDocker struct {
	mu       sync.Mutex
	requests []recordedReq
	containers []ContainerSummary
	networks   []NetworkSummary
	nextID   int
	server   *httptest.Server
}

type recordedReq struct {
	Method string
	Path   string
	Body   map[string]any
}

func newFakeDocker(t *testing.T) (*fakeDocker, *DockerClient) {
	t.Helper()
	f := &fakeDocker{}
	f.server = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.server.Close)
	addr := strings.TrimPrefix(f.server.URL, "http://")
	t.Setenv("DOCKER_HOST", "tcp://"+addr)
	c, err := NewDockerClient()
	if err != nil {
		t.Fatal(err)
	}
	return f, c
}

func (f *fakeDocker) handle(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	if r.Body != nil {
		b, _ := io.ReadAll(r.Body)
		if len(b) > 0 {
			_ = json.Unmarshal(b, &body)
		}
	}
	f.mu.Lock()
	f.requests = append(f.requests, recordedReq{Method: r.Method, Path: r.URL.Path, Body: body})
	f.mu.Unlock()

	p := r.URL.Path
	switch {
	case p == "/_ping":
		w.WriteHeader(200)
	case p == "/images/json":
		json.NewEncoder(w).Encode([]map[string]string{{"Id": "img1"}})
	case p == "/networks/create":
		f.mu.Lock()
		f.nextID++
		id := fmt.Sprintf("net-%d", f.nextID)
		f.mu.Unlock()
		json.NewEncoder(w).Encode(map[string]string{"Id": id})
	case p == "/containers/create":
		f.mu.Lock()
		f.nextID++
		id := fmt.Sprintf("c-%d", f.nextID)
		f.mu.Unlock()
		json.NewEncoder(w).Encode(map[string]string{"Id": id})
	case p == "/containers/json":
		f.mu.Lock()
		list := f.containers
		f.mu.Unlock()
		json.NewEncoder(w).Encode(list)
	case strings.HasPrefix(p, "/containers/") && strings.HasSuffix(p, "/json"):
		json.NewEncoder(w).Encode(map[string]any{"Id": "c", "State": map[string]any{"Status": "running", "Running": true}})
	case p == "/networks":
		f.mu.Lock()
		list := f.networks
		f.mu.Unlock()
		json.NewEncoder(w).Encode(list)
	case strings.HasPrefix(p, "/containers/") && r.Method == http.MethodDelete:
		w.WriteHeader(204)
	case strings.HasPrefix(p, "/networks/") && r.Method == http.MethodDelete:
		w.WriteHeader(204)
	case strings.HasSuffix(p, "/start") || strings.HasSuffix(p, "/stop"):
		w.WriteHeader(204)
	default:
		w.WriteHeader(200)
	}
}

func (f *fakeDocker) find(method, pathPrefix string) []recordedReq {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []recordedReq
	for _, r := range f.requests {
		if r.Method == method && strings.HasPrefix(r.Path, pathPrefix) {
			out = append(out, r)
		}
	}
	return out
}

func TestNetworkCreateIsInternal(t *testing.T) {
	f, c := newFakeDocker(t)
	_, err := c.NetworkCreate(context.Background(), "rd-x", map[string]string{LabelManaged: "true"})
	if err != nil {
		t.Fatal(err)
	}
	reqs := f.find(http.MethodPost, "/networks/create")
	if len(reqs) != 1 {
		t.Fatalf("expected 1 network create, got %d", len(reqs))
	}
	if internal, _ := reqs[0].Body["Internal"].(bool); !internal {
		t.Fatalf("network must be Internal (no egress), body=%v", reqs[0].Body)
	}
}

func TestContainerCreateHardening(t *testing.T) {
	f, c := newFakeDocker(t)
	_, err := c.ContainerCreate(context.Background(), ContainerConfig{
		Image: "busybox", Name: "rd-app-x", Labels: map[string]string{LabelManaged: "true"},
		Network: "rd-x", CPUs: 2, MemoryBytes: 2 << 30,
	})
	if err != nil {
		t.Fatal(err)
	}
	reqs := f.find(http.MethodPost, "/containers/create")
	if len(reqs) != 1 {
		t.Fatalf("expected 1 container create, got %d", len(reqs))
	}
	hc, _ := reqs[0].Body["HostConfig"].(map[string]any)
	if hc == nil {
		t.Fatalf("no HostConfig: %v", reqs[0].Body)
	}
	secOpts, _ := hc["SecurityOpt"].([]any)
	foundNNP := false
	for _, s := range secOpts {
		if s == "no-new-privileges:true" {
			foundNNP = true
		}
	}
	if !foundNNP {
		t.Fatalf("missing no-new-privileges, SecurityOpt=%v", secOpts)
	}
	capDrop, _ := hc["CapDrop"].([]any)
	if len(capDrop) != 1 || capDrop[0] != "ALL" {
		t.Fatalf("CapDrop must be [ALL], got %v", capDrop)
	}
	if hc["NetworkMode"] != "rd-x" {
		t.Fatalf("NetworkMode=%v", hc["NetworkMode"])
	}
	if hc["NanoCpus"] != float64(2e9) {
		t.Fatalf("NanoCpus=%v", hc["NanoCpus"])
	}
	if hc["Memory"] != float64(2<<30) {
		t.Fatalf("Memory=%v", hc["Memory"])
	}
}

func TestTeardownRemovesContainersAndNetwork(t *testing.T) {
	f, c := newFakeDocker(t)
	rt := NewRuntime(c, "01RUN", "drill", nil, nil)
	ctx := context.Background()
	if err := rt.Init(ctx); err != nil {
		t.Fatal(err)
	}
	if err := rt.StartService(ctx, "app", serviceCfg(), ""); err != nil {
		t.Fatal(err)
	}
	rt.Teardown(ctx)

	if len(f.find(http.MethodDelete, "/containers/")) != 1 {
		t.Fatalf("expected 1 container delete, requests=%v", f.requests)
	}
	if len(f.find(http.MethodDelete, "/networks/")) != 1 {
		t.Fatalf("expected 1 network delete")
	}
	// Idempotent: a second teardown must not issue more deletes.
	rt.Teardown(ctx)
	if len(f.find(http.MethodDelete, "/containers/")) != 1 {
		t.Fatalf("teardown not idempotent")
	}
}

func TestGCRemovesInactiveContainersAndNetworks(t *testing.T) {
	f, c := newFakeDocker(t)
	f.containers = []ContainerSummary{
		{ID: "c-active", Labels: map[string]string{LabelKey: "active", LabelManaged: "true"}},
		{ID: "c-old", Labels: map[string]string{LabelKey: "old", LabelManaged: "true"}},
	}
	f.networks = []NetworkSummary{
		{ID: "net-active", Labels: map[string]string{LabelKey: "active", LabelManaged: "true"}},
		{ID: "net-old", Labels: map[string]string{LabelKey: "old", LabelManaged: "true"}},
	}
	n, err := GC(context.Background(), c, []string{"active"}, t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("removed=%d, want 2 (one container + one network)", n)
	}
	var deleted []string
	for _, r := range f.find(http.MethodDelete, "/") {
		deleted = append(deleted, r.Path)
	}
	joined := strings.Join(deleted, " ")
	if !strings.Contains(joined, "c-old") || !strings.Contains(joined, "net-old") {
		t.Fatalf("expected c-old and net-old deleted, got %v", deleted)
	}
	if strings.Contains(joined, "c-active") || strings.Contains(joined, "net-active") {
		t.Fatalf("active resources were deleted: %v", deleted)
	}
}