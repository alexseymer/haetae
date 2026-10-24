// Package server wires the control plane's components into listeners.
package server

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/alexseymer/haetae/internal/gateway"
	"github.com/alexseymer/haetae/internal/pki"
)

// GatewayServer serves the agent gateway over mutual TLS.
type GatewayServer struct {
	srv  *http.Server
	ln   net.Listener
	done chan error
}

// ListenGateway starts serving gw on addr with a server certificate for
// names, issued by ca. It returns once the listener is bound.
func ListenGateway(addr string, gw *gateway.Gateway, ca *pki.CA, names []string) (*GatewayServer, error) {
	cert, err := ca.IssueServerCert(names, 365*24*time.Hour)
	if err != nil {
		return nil, err
	}
	tlsCfg := ca.ServerTLSConfig(cert)

	mux := http.NewServeMux()
	path, h := gw.Handler()
	mux.Handle(path, h)

	srv := &http.Server{
		Handler:           mux,
		TLSConfig:         tlsCfg,
		ReadHeaderTimeout: 10 * time.Second,
		// Agent sessions are long-lived streams: no read or write timeout,
		// but HTTP/2 pings drop connections whose peer has vanished.
		HTTP2: &http.HTTP2Config{
			SendPingTimeout: 30 * time.Second,
			PingTimeout:     15 * time.Second,
		},
	}

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("server: listen gateway: %w", err)
	}
	gs := &GatewayServer{srv: srv, ln: ln, done: make(chan error, 1)}
	go func() {
		err := srv.Serve(tls.NewListener(ln, tlsCfg))
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		gs.done <- err
	}()
	return gs, nil
}

// Addr is the bound address, useful when listening on port 0.
func (gs *GatewayServer) Addr() string { return gs.ln.Addr().String() }

// Done delivers the serve error (nil after a clean shutdown).
func (gs *GatewayServer) Done() <-chan error { return gs.done }

// Close stops the gateway. Agent sessions are streams that never finish on
// their own, so it waits briefly for graceful shutdown and then closes all
// connections; agents reconnect to the next instance.
func (gs *GatewayServer) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := gs.srv.Shutdown(ctx); err != nil {
		return gs.srv.Close()
	}
	return nil
}
