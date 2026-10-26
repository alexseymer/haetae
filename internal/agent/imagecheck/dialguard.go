package imagecheck

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"time"
)

// Registry responses steer the agent elsewhere (redirects, the bearer token
// realm), so a hostile public registry could aim it at host-internal
// services such as the Docker API on 127.0.0.1:2375 or a cloud metadata
// endpoint. Non-public addresses are dialled only when the image's own
// registry is itself non-public (a self-hosted registry the operator chose).

type privateOKKey struct{}

func withPrivateAllowed(ctx context.Context, ok bool) context.Context {
	return context.WithValue(ctx, privateOKKey{}, ok)
}

func privateAllowed(ctx context.Context) bool {
	ok, _ := ctx.Value(privateOKKey{}).(bool)
	return ok
}

// isPublic reports whether addr is a globally routable unicast address.
func isPublic(addr netip.Addr) bool {
	addr = addr.Unmap()
	if !addr.IsGlobalUnicast() || addr.IsPrivate() {
		return false
	}
	for _, p := range nonPublicPrefixes {
		if p.Contains(addr) {
			return false
		}
	}
	return true
}

var nonPublicPrefixes = []netip.Prefix{
	netip.MustParsePrefix("100.64.0.0/10"), // carrier-grade NAT
	netip.MustParsePrefix("192.0.0.0/24"),  // IETF protocol assignments
	netip.MustParsePrefix("198.18.0.0/15"), // benchmarking
	netip.MustParsePrefix("64:ff9b:1::/48"),
}

// registryIsPrivate resolves the registry host of an image ref. Only a
// registry whose every address is non-public counts as private.
func registryIsPrivate(ctx context.Context, host string) bool {
	h := hostOnly(host)
	if a, err := netip.ParseAddr(h); err == nil {
		return !isPublic(a)
	}
	if strings.EqualFold(h, "localhost") {
		return true
	}
	addrs, err := net.DefaultResolver.LookupNetIP(ctx, "ip", h)
	if err != nil || len(addrs) == 0 {
		return false
	}
	for _, a := range addrs {
		if isPublic(a) {
			return false
		}
	}
	return true
}

func hostOnly(host string) string {
	if h, _, err := net.SplitHostPort(host); err == nil {
		return strings.Trim(h, "[]")
	}
	return strings.Trim(host, "[]")
}

var errBlockedAddr = errors.New("registry: refusing to connect to a non-public address")

func guardedDial(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	var addrs []netip.Addr
	if a, err := netip.ParseAddr(host); err == nil {
		addrs = []netip.Addr{a}
	} else {
		addrs, err = net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		if err != nil {
			return nil, err
		}
	}
	allowPrivate := privateAllowed(ctx)
	d := net.Dialer{Timeout: 10 * time.Second}
	lastErr := fmt.Errorf("%w: %s", errBlockedAddr, host)
	for _, a := range addrs {
		if !allowPrivate && !isPublic(a) {
			continue
		}
		// Dial the checked address itself so a second DNS answer cannot
		// swap in a different one.
		c, err := d.DialContext(ctx, network, net.JoinHostPort(a.Unmap().String(), port))
		if err == nil {
			return c, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

func newGuardedTransport() *http.Transport {
	return &http.Transport{
		Proxy:                 nil, // the agent has no proxy support; a proxy would bypass the guard
		DialContext:           guardedDial,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          4,
		IdleConnTimeout:       30 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 15 * time.Second,
	}
}
