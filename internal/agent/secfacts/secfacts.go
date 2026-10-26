// Package secfacts derives host security misconfigurations from Docker
// inspect and TLS probes. Container env values are never read.
package secfacts

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/alexseymer/haetae/internal/agent/dockerapi"
	agentv1 "github.com/alexseymer/haetae/internal/gen/haetae/agent/v1"
)

// Misconfig kinds (match proto / design).
const (
	KindPublishedPort       = "published_port"
	KindDockerSocket        = "docker_socket"
	KindRootUser            = "root_user"
	KindPrivileged          = "privileged"
	KindHostNetwork         = "host_network"
	KindHostPID             = "host_pid"
	KindDangerousCapability = "dangerous_capability"
	KindCertExpiring        = "cert_expiring"
)

// highPublishPorts are well-known database / Docker daemon ports that
// warrant high severity when published on 0.0.0.0/::.
var highPublishPorts = map[uint16]bool{
	5432: true, 3306: true, 6379: true, 27017: true,
	9200: true, 11211: true, 2375: true, 2376: true,
}

// dangerousCaps maps capability names to severity.
var dangerousCaps = map[string]string{
	"SYS_ADMIN":  "high",
	"NET_ADMIN":  "high",
	"SYS_PTRACE": "medium",
	"ALL":        "high",
}

// CaddyPorts are the host ports owned by the managed Caddy ingress; published
// bindings on those ports for the Caddy container are not reported.
type CaddyPorts struct {
	ContainerName string // empty = match nothing by name
	HTTPPort      int
	HTTPSPort     int
}

// Options tunes Collect. Zero values get defaults.
type Options struct {
	Caddy      CaddyPorts
	Domains    []string      // managed ingress domains to TLS-probe
	TLSTimeout time.Duration // default 3s
	Now        func() time.Time
	DialTLS    func(network, addr string, cfg *tls.Config) (net.Conn, error) // tests
}

func (o Options) withDefaults() Options {
	if o.TLSTimeout <= 0 {
		o.TLSTimeout = 3 * time.Second
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.DialTLS == nil {
		o.DialTLS = func(network, addr string, cfg *tls.Config) (net.Conn, error) {
			d := &tls.Dialer{Config: cfg, NetDialer: &net.Dialer{Timeout: o.TLSTimeout}}
			return d.Dial(network, addr)
		}
	}
	return o
}

// Collect inspects every container and returns misconfigs. It never reads env.
// Any inspect error other than "not found" fails the whole collection, because
// the server treats the resulting SecurityReport as a full replacement.
func Collect(ctx context.Context, docker *dockerapi.Client, opts Options) ([]*agentv1.Misconfig, error) {
	opts = opts.withDefaults()
	list, err := docker.ListContainers(ctx, true)
	if err != nil {
		return nil, err
	}
	var out []*agentv1.Misconfig
	var tlsTargets []tlsTarget
	for _, c := range list {
		d, err := docker.InspectContainer(ctx, c.ID)
		if err != nil {
			if strings.Contains(err.Error(), "status 404") {
				continue // removed since the list: genuinely gone
			}
			// The report is a full replacement on the server: a partial one would
			// mark the missing container's misconfigs resolved. Fail whole.
			return nil, fmt.Errorf("secfacts: inspect %s: %w", c.ID, err)
		}
		out = append(out, FromInspect(c.ID, d, opts.Caddy)...)
		tlsTargets = append(tlsTargets, publishedTLSTargets(c.ID, d)...)
	}
	for _, domain := range opts.Domains {
		out = append(out, probeCert(ctx, opts, "", domain+":443", domain)...)
	}
	seen := make(map[string]bool)
	for _, t := range tlsTargets {
		key := t.addr
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, probeCert(ctx, opts, t.containerID, t.addr, t.serverName)...)
	}
	return out, nil
}

// FromInspect derives misconfigs for one container from its inspect JSON.
// Pure and suitable for unit tests.
func FromInspect(containerID string, d dockerapi.ContainerDetail, caddy CaddyPorts) []*agentv1.Misconfig {
	id := shortID(containerID, d.ID)
	var out []*agentv1.Misconfig

	if d.HostConfig.Privileged {
		out = append(out, mc(id, KindPrivileged, "high", "privileged=true"))
	}
	if isHostMode(d.HostConfig.NetworkMode) {
		out = append(out, mc(id, KindHostNetwork, "medium", "network_mode=host"))
	}
	if isHostMode(d.HostConfig.PidMode) {
		out = append(out, mc(id, KindHostPID, "medium", "pid_mode=host"))
	}
	if isRootUser(d.Config.User) {
		out = append(out, mc(id, KindRootUser, "low", "user=root"))
	}
	for _, cap := range d.HostConfig.CapAdd {
		name := normalizeCap(cap)
		if sev, ok := dangerousCaps[name]; ok {
			out = append(out, mc(id, KindDangerousCapability, sev, "cap="+name))
		}
	}
	if hasDockerSocket(d) {
		out = append(out, mc(id, KindDockerSocket, "critical", "mount=/var/run/docker.sock"))
	}
	out = append(out, publishedPorts(id, d, caddy)...)
	return out
}

func publishedPorts(id string, d dockerapi.ContainerDetail, caddy CaddyPorts) []*agentv1.Misconfig {
	var out []*agentv1.Misconfig
	seen := make(map[string]bool)
	add := func(key, hostIP, hostPort string) {
		ip := strings.TrimSpace(hostIP)
		if ip == "" {
			ip = "0.0.0.0"
		}
		if !isWorldBind(ip) {
			return
		}
		pub, err := strconv.ParseUint(hostPort, 10, 16)
		if err != nil || pub == 0 {
			return
		}
		p := uint16(pub)
		if skipCaddyPublish(d, caddy, p) {
			return
		}
		proto := "tcp"
		priv := p
		if i := strings.LastIndex(key, "/"); i >= 0 {
			proto = key[i+1:]
			if n, err := strconv.ParseUint(key[:i], 10, 16); err == nil {
				priv = uint16(n)
			}
		} else if n, err := strconv.ParseUint(key, 10, 16); err == nil {
			priv = uint16(n)
		}
		detail := fmt.Sprintf("%s:%d/%s", normalizeBindIP(ip), p, strings.ToLower(proto))
		if seen[detail] {
			return
		}
		seen[detail] = true
		sev := "medium"
		if highPublishPorts[priv] || highPublishPorts[p] {
			sev = "high"
		}
		out = append(out, mc(id, KindPublishedPort, sev, detail))
	}

	for key, binds := range d.HostConfig.PortBindings {
		for _, b := range binds {
			add(key, b.HostIP, b.HostPort)
		}
	}
	for key, binds := range d.NetworkSettings.Ports {
		for _, b := range binds {
			add(key, b.HostIP, b.HostPort)
		}
	}
	return out
}

func skipCaddyPublish(d dockerapi.ContainerDetail, caddy CaddyPorts, port uint16) bool {
	if caddy.ContainerName == "" {
		return false
	}
	name := strings.TrimPrefix(d.Name, "/")
	if name != caddy.ContainerName {
		return false
	}
	return int(port) == caddy.HTTPPort || int(port) == caddy.HTTPSPort
}

func hasDockerSocket(d dockerapi.ContainerDetail) bool {
	for _, m := range d.Mounts {
		if strings.Contains(m.Source, "docker.sock") || strings.Contains(m.Destination, "docker.sock") {
			return true
		}
	}
	for _, b := range d.HostConfig.Binds {
		if strings.Contains(b, "docker.sock") {
			return true
		}
	}
	return false
}

func isRootUser(user string) bool {
	user = strings.TrimSpace(user)
	if user == "" || user == "0" || user == "root" || user == "0:0" {
		return true
	}
	if i := strings.IndexByte(user, ':'); i > 0 {
		return user[:i] == "0" || user[:i] == "root"
	}
	return false
}

func isHostMode(mode string) bool {
	mode = strings.ToLower(strings.TrimSpace(mode))
	return mode == "host"
}

func normalizeCap(c string) string {
	c = strings.TrimSpace(strings.ToUpper(c))
	c = strings.TrimPrefix(c, "CAP_")
	return c
}

func isWorldBind(ip string) bool {
	ip = strings.TrimSpace(ip)
	switch ip {
	case "", "0.0.0.0", "::", "[::]":
		return true
	}
	if strings.HasPrefix(ip, "[") && strings.HasSuffix(ip, "]") {
		ip = ip[1 : len(ip)-1]
	}
	addr := net.ParseIP(ip)
	if addr == nil {
		return false
	}
	return addr.IsUnspecified()
}

func normalizeBindIP(ip string) string {
	if ip == "::" || ip == "[::]" {
		return "::"
	}
	return ip
}

func shortID(preferred, fallback string) string {
	if preferred != "" {
		return preferred
	}
	return fallback
}

func mc(containerID, kind, severity, detail string) *agentv1.Misconfig {
	return &agentv1.Misconfig{
		ContainerId: containerID,
		Kind:        kind,
		Severity:    severity,
		Detail:      detail,
	}
}

type tlsTarget struct {
	containerID string
	addr        string
	serverName  string
}

func publishedTLSTargets(containerID string, d dockerapi.ContainerDetail) []tlsTarget {
	var out []tlsTarget
	add := func(hostIP, hostPort string) {
		port, err := strconv.Atoi(hostPort)
		if err != nil || port <= 0 {
			return
		}
		// Probe published TLS-looking ports (443 and high DB TLS ports are
		// covered via domain probes / published_port; here we probe host
		// bindings that look like TLS: 443, or any world-bound port where
		// we can attempt a short TLS handshake).
		if port != 443 && port != 8443 && port != 9443 {
			return
		}
		ip := hostIP
		if ip == "" || isWorldBind(ip) {
			ip = "127.0.0.1"
		}
		if strings.HasPrefix(ip, "[") {
			ip = strings.Trim(ip, "[]")
		}
		addr := net.JoinHostPort(ip, strconv.Itoa(port))
		out = append(out, tlsTarget{containerID: containerID, addr: addr, serverName: "localhost"})
	}
	for _, binds := range d.NetworkSettings.Ports {
		for _, b := range binds {
			add(b.HostIP, b.HostPort)
		}
	}
	for _, binds := range d.HostConfig.PortBindings {
		for _, b := range binds {
			add(b.HostIP, b.HostPort)
		}
	}
	return out
}

func probeCert(ctx context.Context, opts Options, containerID, addr, serverName string) []*agentv1.Misconfig {
	cfg := &tls.Config{
		InsecureSkipVerify: true, //nolint:gosec // expiry probe only; not authenticating
		ServerName:         serverName,
		MinVersion:         tls.VersionTLS12,
	}
	type result struct {
		conn net.Conn
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		conn, err := opts.DialTLS("tcp", addr, cfg)
		ch <- result{conn, err}
	}()
	var res result
	select {
	case <-ctx.Done():
		return nil
	case <-time.After(opts.TLSTimeout):
		return nil
	case res = <-ch:
	}
	if res.err != nil || res.conn == nil {
		return nil
	}
	defer func() { _ = res.conn.Close() }()
	tlsConn, ok := res.conn.(*tls.Conn)
	if !ok {
		return nil
	}
	state := tlsConn.ConnectionState()
	if len(state.PeerCertificates) == 0 {
		return nil
	}
	cert := state.PeerCertificates[0]
	now := opts.Now()
	remaining := cert.NotAfter.Sub(now)
	var sev, detail string
	switch {
	case remaining < 0:
		sev = "critical"
		detail = fmt.Sprintf("cert expired on %s (%s)", cert.NotAfter.UTC().Format("2006-01-02"), serverName)
	case remaining < 3*24*time.Hour:
		sev = "critical"
		detail = fmt.Sprintf("cert expires in <%dd (%s)", 3, serverName)
	case remaining < 14*24*time.Hour:
		sev = "high"
		detail = fmt.Sprintf("cert expires in <%dd (%s)", 14, serverName)
	default:
		return nil
	}
	return []*agentv1.Misconfig{mc(containerID, KindCertExpiring, sev, detail)}
}
