package server

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"time"
)

// WebServer serves the web interface, the JSON API and the install endpoints
// over HTTPS.
type WebServer struct {
	srv  *http.Server
	ln   net.Listener
	done chan error
}

// ListenWeb starts serving handler on addr with cert. It returns once the
// listener is bound.
func ListenWeb(addr string, handler http.Handler, cert tls.Certificate) (*WebServer, error) {
	tlsCfg := &tls.Config{
		Certificates: []tls.Certificate{cert},
		MinVersion:   tls.VersionTLS12,
		NextProtos:   []string{"h2", "http/1.1"},
	}
	srv := &http.Server{
		Handler:           handler,
		TLSConfig:         tlsCfg,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    32 << 10,
	}
	ln, err := new(net.ListenConfig).Listen(context.Background(), "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("server: listen web: %w", err)
	}
	ws := &WebServer{srv: srv, ln: ln, done: make(chan error, 1)}
	go func() {
		err := srv.Serve(tls.NewListener(ln, tlsCfg))
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		ws.done <- err
	}()
	return ws, nil
}

// Addr is the bound address.
func (ws *WebServer) Addr() string { return ws.ln.Addr().String() }

// Done delivers the serve error (nil after a clean shutdown).
func (ws *WebServer) Done() <-chan error { return ws.done }

// Close stops the server, waiting briefly for requests in flight.
func (ws *WebServer) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := ws.srv.Shutdown(ctx); err != nil {
		return ws.srv.Close()
	}
	return nil
}
