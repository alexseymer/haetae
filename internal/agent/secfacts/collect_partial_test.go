package secfacts

import (
	"context"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alexseymer/haetae/internal/agent/dockerapi"
)

func fakeDocker(t *testing.T, inspectStatus map[string]int) *dockerapi.Client {
	t.Helper()
	sock := filepath.Join(t.TempDir(), "d.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Skip("unix sockets unavailable")
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/containers/json"):
			_, _ = w.Write([]byte(`[{"Id":"aaa","Image":"x"},{"Id":"bbb","Image":"x"}]`))
		case strings.HasSuffix(r.URL.Path, "/json"):
			id := strings.Split(r.URL.Path, "/")[len(strings.Split(r.URL.Path, "/"))-2]
			if st := inspectStatus[id]; st != 0 {
				w.WriteHeader(st)
				_, _ = w.Write([]byte(`{"message":"boom"}`))
				return
			}
			_, _ = w.Write([]byte(`{"Id":"` + id + `","HostConfig":{"Privileged":true},"Config":{}}`))
		default:
			http.NotFound(w, r)
		}
	})
	srv := &http.Server{Handler: mux}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return dockerapi.New(sock)
}

func TestCollectFailsWholeOnInspectError(t *testing.T) {
	d := fakeDocker(t, map[string]int{"bbb": 500})
	if ms, err := Collect(context.Background(), d, Options{}); err == nil {
		t.Fatalf("want error, got partial result of %d misconfigs", len(ms))
	}
}

func TestCollectSkipsVanishedContainer(t *testing.T) {
	d := fakeDocker(t, map[string]int{"bbb": 404})
	ms, err := Collect(context.Background(), d, Options{})
	if err != nil || len(ms) == 0 {
		t.Fatalf("ms=%d err=%v", len(ms), err)
	}
}
