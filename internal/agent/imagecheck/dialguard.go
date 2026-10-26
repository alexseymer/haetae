package imagecheck

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"slices"
	"strings"
	"time"
)

// Registry responses steer the agent elsewhere (redirects, the bearer token
// realm), so a hostile public registry could aim it at host-internal
// services such as the Docker API on 127.0.0.1:2375 or a cloud metadata
// endpoint. Non-public addresses are dialled only when the image's own
// registry is itself non-public (a self-hosted registry the operator chose).

// privateAllowance pins the one non-public destination a request may reach:
// the image ref's own registry host, at the addresses resolved once up front
// (so a DNS answer that changes later cannot widen it). Every other host,
// including redirect targets and token realms, must be public when dialled.
type privateAllowance struct {
	host  string
	addrs []netip.Addr
}

type privateOKKey struct{}

func withPrivateAllowance(ctx context.Context, a *privateAllowance) context.Context {
	return context.WithValue(ctx, privateOKKey{}, a)
}

func allowanceFrom(ctx context.Context) *privateAllowance {
	a, _ := ctx.Value(privateOKKey{}).(*privateAllowance)
	return a
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

// Ranges Go's IsGlobalUnicast/IsPrivate still accept. The IPv6 translation
// and tunnel prefixes embed IPv4 addresses (64:ff9b::7f00:1 is 127.0.0.1 via
// NAT64, 2002:7f00:1:: via 6to4), so they are refused wholesale.
var nonPublicPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),       // "this network"; Linux routes 0.x to localhost
	netip.MustParsePrefix("100.64.0.0/10"),   // carrier-grade NAT (also cloud metadata on some clouds)
	netip.MustParsePrefix("192.0.0.0/24"),    // IETF protocol assignments
	netip.MustParsePrefix("192.0.2.0/24"),    // TEST-NET-1
	netip.MustParsePrefix("198.18.0.0/15"),   // benchmarking
	netip.MustParsePrefix("198.51.100.0/24"), // TEST-NET-2
	netip.MustParsePrefix("203.0.113.0/24"),  // TEST-NET-3
	netip.MustParsePrefix("240.0.0.0/4"),     // reserved
	netip.MustParsePrefix("64:ff9b::/96"),    // NAT64 well-known prefix
	netip.MustParsePrefix("64:ff9b:1::/48"),  // local-use NAT64
	netip.MustParsePrefix("2001::/32"),       // Teredo
	netip.MustParsePrefix("2001:db8::/32"),   // documentation
	netip.MustParsePrefix("2002::/16"),       // 6to4
}

// privateRegistry resolves the registry host of an image ref once. Only a
// registry whose every address is non-public gets an allowance (a self-hosted
// registry the operator chose); a name with any public address is treated as
// public, so mixed answers cannot be used to reach private space.
func privateRegistry(ctx context.Context, host string) *privateAllowance {
	h := hostOnly(host)
	var addrs []netip.Addr
	if a, err := netip.ParseAddr(h); err == nil {
		addrs = []netip.Addr{a.Unmap()}
	} else {
		var err error
		addrs, err = net.DefaultResolver.LookupNetIP(ctx, "ip", h)
		if err != nil || len(addrs) == 0 {
			return nil
		}
		for i := range addrs {
			addrs[i] = addrs[i].Unmap()
		}
	}
	for _, a := range addrs {
		if isPublic(a) {
			return nil
		}
	}
	return &privateAllowance{host: strings.ToLower(h), addrs: addrs}
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
	allow := allowanceFrom(ctx)
	if allow != nil && !strings.EqualFold(host, allow.host) {
		allow = nil // only the image's own registry host, not realms or redirects
	}
	d := net.Dialer{Timeout: 10 * time.Second}
	lastErr := fmt.Errorf("%w: %s", errBlockedAddr, host)
	for _, a := range addrs {
		a = a.Unmap()
		if !isPublic(a) && (allow == nil || !slices.Contains(allow.addrs, a)) {
			continue
		}
		// Dial the checked address itself so a second DNS answer cannot
		// swap in a different one.
		c, err := d.DialContext(ctx, network, net.JoinHostPort(a.String(), port))
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
		DisableKeepAlives:     true, // connections are never shared between allowance contexts
		IdleConnTimeout:       30 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 15 * time.Second,
	}
}
