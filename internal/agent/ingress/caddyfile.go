package ingress

import (
	"fmt"
	"sort"
	"strings"
)

// writeCaddyfileContent builds a deterministic Caddyfile from routes.
// Callers must validate domain and upstream before adding them to routes;
// this function only emits already-validated values (never raw user input).
//
// auto_https is left at Caddy defaults (on). http_port/https_port are always
// 80/443 inside the container; host publishing is handled by docker -p.
// reverse_proxy keeps Caddy's default X-Forwarded-* behaviour.
func writeCaddyfileContent(routes map[string]route, tlsInternal bool) string {
	type site struct {
		domain   string
		upstream string
	}
	sites := make([]site, 0, len(routes))
	for _, r := range routes {
		sites = append(sites, site{domain: r.Domain, upstream: r.Upstream})
	}
	sort.Slice(sites, func(i, j int) bool {
		return sites[i].domain < sites[j].domain
	})

	var b strings.Builder
	b.WriteString("{\n")
	b.WriteString("\tadmin unix//run/caddy-admin.sock\n")
	b.WriteString("\thttp_port 80\n")
	b.WriteString("\thttps_port 443\n")
	b.WriteString("\tservers {\n")
	b.WriteString("\t\tprotocols h1 h2\n")
	b.WriteString("\t}\n")
	b.WriteString("}\n")

	for _, s := range sites {
		b.WriteByte('\n')
		b.WriteString(s.domain)
		b.WriteString(" {\n")
		fmt.Fprintf(&b, "\treverse_proxy %s\n", s.upstream)
		if tlsInternal {
			b.WriteString("\ttls internal\n")
		}
		b.WriteString("}\n")
	}
	return b.String()
}
