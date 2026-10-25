package ingress

import (
	"strings"
	"testing"
)

func TestWriteCaddyfileContent_Empty(t *testing.T) {
	got := writeCaddyfileContent(nil, false)
	want := "{\n\tadmin unix//run/caddy-admin.sock\n\thttp_port 80\n\thttps_port 443\n\tservers {\n\t\tprotocols h1 h2\n\t}\n}\n"
	if got != want {
		t.Fatalf("empty:\n got: %q\nwant: %q", got, want)
	}
	if !strings.Contains(got, "admin unix//run/caddy-admin.sock") {
		t.Fatal("missing admin unix socket")
	}
}

func TestWriteCaddyfileContent_OrderAndTLS(t *testing.T) {
	routes := map[string]route{
		"zeta":  {Domain: "zeta.example.com", Upstream: "zeta:9000"},
		"alpha": {Domain: "alpha.example.com", Upstream: "alpha:80"},
		"mid":   {Domain: "mid.example.com", Upstream: "mid:8080"},
	}

	got := writeCaddyfileContent(routes, false)
	want := `{
	admin unix//run/caddy-admin.sock
	http_port 80
	https_port 443
	servers {
		protocols h1 h2
	}
}

alpha.example.com {
	reverse_proxy alpha:80
}

mid.example.com {
	reverse_proxy mid:8080
}

zeta.example.com {
	reverse_proxy zeta:9000
}
`
	if got != want {
		t.Fatalf("without tls:\n got:\n%s\nwant:\n%s", got, want)
	}

	gotTLS := writeCaddyfileContent(routes, true)
	wantTLS := `{
	admin unix//run/caddy-admin.sock
	http_port 80
	https_port 443
	servers {
		protocols h1 h2
	}
}

alpha.example.com {
	reverse_proxy alpha:80
	tls internal
}

mid.example.com {
	reverse_proxy mid:8080
	tls internal
}

zeta.example.com {
	reverse_proxy zeta:9000
	tls internal
}
`
	if gotTLS != wantTLS {
		t.Fatalf("with tls:\n got:\n%s\nwant:\n%s", gotTLS, wantTLS)
	}
}
