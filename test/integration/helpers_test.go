//go:build integration

package integration

import (
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/alexseymer/haetae/internal/pki"
)

// httpClientNoCert talks to the gateway with TLS but no client certificate.
func httpClientNoCert(pin string) *http.Client {
	return httpClientTLS(pki.PinnedTLSConfig(pin, "127.0.0.1"))
}

func httpClientTLS(cfg *tls.Config) *http.Client {
	tr := &http.Transport{TLSClientConfig: cfg, ForceAttemptHTTP2: true}
	tr.Protocols = new(http.Protocols)
	tr.Protocols.SetHTTP2(true)
	return &http.Client{Transport: tr, Timeout: 10 * time.Second}
}

// proxy is a TCP relay whose link can be cut and restored, to simulate a
// network outage between agent and gateway.
type proxy struct {
	t      *testing.T
	addr   string
	target string

	mu     sync.Mutex
	ln     net.Listener
	conns  map[net.Conn]struct{}
	silent bool // blackhole: swallow bytes, never close

	// Agent-side connections, and those the agent itself closed (we read
	// EOF or a reset from it, rather than closing it ourselves).
	agentConns  map[net.Conn]bool
	agentClosed map[net.Conn]bool
}

func newProxy(t *testing.T, target string) *proxy {
	t.Helper()
	p := &proxy{t: t, addr: freeAddr(t), target: target, conns: map[net.Conn]struct{}{},
		agentConns: map[net.Conn]bool{}, agentClosed: map[net.Conn]bool{}}
	p.restore()
	t.Cleanup(p.cut)
	return p
}

// cut closes the listener and drops every relayed connection.
func (p *proxy) cut() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.ln != nil {
		p.ln.Close()
		p.ln = nil
	}
	for c := range p.conns {
		c.Close()
		delete(p.conns, c)
	}
}

// restore listens again on the same address.
func (p *proxy) restore() {
	p.t.Helper()
	ln, err := net.Listen("tcp", p.addr)
	if err != nil {
		p.t.Fatalf("proxy listen: %v", err)
	}
	p.mu.Lock()
	p.ln = ln
	p.mu.Unlock()
	go p.accept(ln)
}

func (p *proxy) accept(ln net.Listener) {
	for {
		in, err := ln.Accept()
		if err != nil {
			return
		}
		out, err := net.Dial("tcp", p.target)
		if err != nil {
			in.Close()
			continue
		}
		p.mu.Lock()
		if p.ln != ln { // cut while dialing
			p.mu.Unlock()
			in.Close()
			out.Close()
			return
		}
		p.conns[in] = struct{}{}
		p.conns[out] = struct{}{}
		p.agentConns[in] = true
		p.mu.Unlock()
		go p.relay(in, out)
		go p.relay(out, in)
	}
}

// blackhole keeps every connection open but silently drops all traffic,
// like a network path that loses packets without sending resets.
func (p *proxy) blackhole(on bool) {
	p.mu.Lock()
	p.silent = on
	p.mu.Unlock()
}

func (p *proxy) dropping() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.silent
}

// agentSideConns returns the agent-side connections open right now.
func (p *proxy) agentSideConns() []net.Conn {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []net.Conn
	for c := range p.agentConns {
		if !p.agentClosed[c] {
			out = append(out, c)
		}
	}
	return out
}

// closedByAgent reports whether the agent closed c (as opposed to the proxy
// or the server side tearing it down).
func (p *proxy) closedByAgent(c net.Conn) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.agentClosed[c]
}

func (p *proxy) relay(dst, src net.Conn) {
	defer dst.Close()
	defer src.Close()
	buf := make([]byte, 32*1024)
	for {
		n, err := src.Read(buf)
		if n > 0 && !p.dropping() {
			if _, werr := dst.Write(buf[:n]); werr != nil {
				return
			}
		}
		if err != nil {
			if !errors.Is(err, net.ErrClosed) {
				p.mu.Lock()
				if p.agentConns[src] {
					p.agentClosed[src] = true
				}
				p.mu.Unlock()
			}
			return
		}
	}
}
