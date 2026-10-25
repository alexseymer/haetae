package chat_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/alexseymer/haetae/internal/chat"
)

func TestHTTPClientRefusesRedirect(t *testing.T) {
	var followedPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/botTOKEN/getMe" {
			http.Redirect(w, r, "/stolen", http.StatusFound)
			return
		}
		followedPath = r.URL.Path
		_, _ = w.Write([]byte(`ok`))
	}))
	defer srv.Close()

	cli := chat.NewHTTPClient(5 * time.Second)
	resp, err := cli.Get(srv.URL + "/botTOKEN/getMe")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("status=%d want 302", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = body
	if followedPath != "" {
		t.Fatalf("client followed redirect to %q (token leak risk)", followedPath)
	}
}
