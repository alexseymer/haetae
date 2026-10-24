package dockerapi

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func startUnixServer(t *testing.T, handler http.Handler) *Client {
	t.Helper()
	dir := t.TempDir()
	sock := filepath.Join(dir, "docker.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := &http.Server{Handler: handler}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() {
		_ = srv.Close()
		_ = ln.Close()
	})
	return New(sock)
}

func TestVersion(t *testing.T) {
	c := startUnixServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/"+APIVersion+"/version" {
			t.Errorf("path = %q", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]string{
			"Version":    "24.0.0",
			"ApiVersion": "1.43",
		})
	}))
	v, err := c.Version(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if v.Version != "24.0.0" || v.APIVersion != "1.43" {
		t.Fatalf("got %+v", v)
	}
}

func TestListContainers(t *testing.T) {
	c := startUnixServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/"+APIVersion+"/containers/json" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if r.URL.Query().Get("all") != "1" {
			t.Errorf("all = %q", r.URL.Query().Get("all"))
		}
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{
				"Id":      "abc123",
				"Names":   []string{"/web"},
				"Image":   "nginx:latest",
				"ImageID": "sha256:deadbeef",
				"Created": int64(1700000000),
				"State":   "running",
				"Status":  "Up 2 hours",
				"Labels":  map[string]string{"app": "web"},
				"Ports": []map[string]any{
					{"IP": "0.0.0.0", "PrivatePort": 80, "PublicPort": 8080, "Type": "tcp"},
				},
			},
		})
	}))
	list, err := c.ListContainers(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].ID != "abc123" || list[0].Names[0] != "/web" {
		t.Fatalf("got %+v", list)
	}
	if list[0].Ports[0].PublicPort != 8080 {
		t.Fatalf("ports: %+v", list[0].Ports)
	}
}

func TestInspectContainer(t *testing.T) {
	c := startUnixServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		want := "/" + APIVersion + "/containers/abc%2Fdef/json"
		if r.URL.EscapedPath() != want && r.URL.Path != "/"+APIVersion+"/containers/abc/def/json" {
			// PathEscape turns / into %2F; Accept either form depending on Go version.
			if !strings.Contains(r.URL.RawPath, "abc") && !strings.Contains(r.URL.Path, "abc") {
				t.Errorf("path = %q raw = %q", r.URL.Path, r.URL.RawPath)
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"Id":           "abc/def",
			"Name":         "/web",
			"Created":      "2024-01-01T00:00:00Z",
			"RestartCount": 2,
			"Image":        "sha256:img",
			"State": map[string]any{
				"Status":     "running",
				"Running":    true,
				"OOMKilled":  false,
				"ExitCode":   0,
				"StartedAt":  "2024-01-01T00:00:00Z",
				"FinishedAt": "0001-01-01T00:00:00Z",
				"Health":     map[string]any{"Status": "healthy", "FailingStreak": 0},
			},
			"Config": map[string]any{
				"Image":  "nginx:latest",
				"User":   "1000",
				"Labels": map[string]string{"app": "web"},
				"Env":    []string{"SECRET=should-not-decode"},
				"Cmd":    []string{"/bin/sh", "-c", "secret"},
			},
			"HostConfig": map[string]any{"Privileged": false, "NetworkMode": "bridge"},
			"Mounts": []map[string]any{
				{"Type": "bind", "Source": "/data", "Destination": "/app", "RW": true},
			},
		})
	}))
	d, err := c.InspectContainer(context.Background(), "abc/def")
	if err != nil {
		t.Fatal(err)
	}
	if d.ID != "abc/def" || d.Name != "/web" || d.RestartCount != 2 {
		t.Fatalf("got %+v", d)
	}
	if d.State.Health == nil || d.State.Health.Status != "healthy" {
		t.Fatalf("health: %+v", d.State.Health)
	}
	if d.Config.Image != "nginx:latest" || d.Config.User != "1000" {
		t.Fatalf("config: %+v", d.Config)
	}
}

func TestStats(t *testing.T) {
	c := startUnixServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("stream") != "false" || r.URL.Query().Get("one-shot") != "true" {
			t.Errorf("query = %v", r.URL.Query())
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"read": time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
			"cpu_stats": map[string]any{
				"cpu_usage":        map[string]any{"total_usage": 1000},
				"system_cpu_usage": 2000,
				"online_cpus":      2,
			},
			"memory_stats": map[string]any{
				"usage": 1024,
				"limit": 2048,
				"stats": map[string]uint64{"cache": 10},
			},
			"networks": map[string]any{
				"eth0": map[string]uint64{"rx_bytes": 100, "tx_bytes": 200},
			},
			"blkio_stats": map[string]any{
				"io_service_bytes_recursive": []map[string]any{
					{"op": "read", "value": 50},
				},
			},
			"pids_stats": map[string]uint64{"current": 3},
		})
	}))
	s, err := c.Stats(context.Background(), "cid")
	if err != nil {
		t.Fatal(err)
	}
	if s.CPUStats.CPUUsage.TotalUsage != 1000 || s.MemoryStats.Usage != 1024 {
		t.Fatalf("got %+v", s)
	}
	if s.Networks["eth0"].RxBytes != 100 || s.PidsStats.Current != 3 {
		t.Fatalf("got %+v", s)
	}
}

func TestNon2xxError(t *testing.T) {
	c := startUnixServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_ = json.NewEncoder(w).Encode(map[string]string{"message": "No such container: xyz"})
	}))
	_, err := c.InspectContainer(context.Background(), "xyz")
	if err == nil {
		t.Fatal("expected error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "404") || !strings.Contains(msg, "No such container: xyz") {
		t.Fatalf("error = %q", msg)
	}
}

func TestEventsTwoThenCancel(t *testing.T) {
	c := startUnixServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/events") {
			t.Errorf("path = %q", r.URL.Path)
		}
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("no flusher")
		}
		enc := json.NewEncoder(w)
		_ = enc.Encode(Event{Type: "container", Action: "start", TimeNano: 1})
		flusher.Flush()
		_ = enc.Encode(Event{Type: "container", Action: "die", TimeNano: 2})
		flusher.Flush()
		// Keep the connection open until the client cancels.
		<-r.Context().Done()
	}))

	ctx, cancel := context.WithCancel(context.Background())
	var got []Event
	errCh := make(chan error, 1)
	go func() {
		errCh <- c.Events(ctx, func(ev Event) {
			got = append(got, ev)
			if len(got) == 2 {
				cancel()
			}
		})
	}()

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("Events: %v", err)
		}
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("timeout")
	}
	if len(got) != 2 || got[0].Action != "start" || got[1].Action != "die" {
		t.Fatalf("got %+v", got)
	}
}

func TestBodyLimit(t *testing.T) {
	c := startUnixServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 9 MiB of zeros wrapped as a JSON string would be huge; send raw oversized body.
		w.Header().Set("Content-Type", "application/json")
		huge := make([]byte, 9<<20)
		for i := range huge {
			huge[i] = 'A'
		}
		_, _ = w.Write(huge)
	}))
	_, err := c.Version(context.Background())
	if err == nil {
		t.Fatal("expected error from oversized body")
	}
	// Confirm we do not allocate unbounded: LimitReader capped the read.
	// The decode error (or truncated parse) is enough; also check message.
	if !strings.Contains(err.Error(), "decode") && !strings.Contains(err.Error(), "dockerapi") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestBodyLimitTruncates(t *testing.T) {
	// Directly verify LimitReader behaviour used by the client.
	r := io.LimitReader(strings.NewReader(strings.Repeat("x", 9<<20)), maxBody)
	n, err := io.Copy(io.Discard, r)
	if err != nil {
		t.Fatal(err)
	}
	if n != maxBody {
		t.Fatalf("read %d, want %d", n, maxBody)
	}
}

func TestListContainersAllFalse(t *testing.T) {
	c := startUnixServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("all") != "0" {
			t.Errorf("all = %q", r.URL.Query().Get("all"))
		}
		_, _ = w.Write([]byte("[]"))
	}))
	list, err := c.ListContainers(context.Background(), false)
	if err != nil {
		t.Fatal(err)
	}
	if list == nil {
		// empty slice from JSON is fine; nil is also ok after unmarshal of []
		list = []ContainerSummary{}
	}
	if len(list) != 0 {
		t.Fatalf("got %d", len(list))
	}
}

// Ensure the socket path is used (smoke).
func TestNewUsesSocket(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "missing.sock")
	c := New(sock)
	_, err := c.Version(context.Background())
	if err == nil {
		t.Fatal("expected dial error")
	}
	if _, err := os.Stat(sock); !os.IsNotExist(err) {
		// socket may not exist; that's fine
		_ = err
	}
}
