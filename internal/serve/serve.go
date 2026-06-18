// Package serve is the shared HTTP serving entrypoint for the hermEX daemons. It
// terminates TLS when the configuration supplies a certificate and falls back to
// plaintext otherwise, so a daemon gains HTTPS by configuration alone — without
// each command duplicating the TLS-versus-plaintext decision or the hardened
// config.TLSConfig (TLS 1.2 floor).
package serve

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"

	"hermex/internal/config"
)

// Server is a bound HTTP server ready to start and shut down gracefully. It
// satisfies lifecycle.Component (Start blocks serving; Shutdown drains in-flight
// requests within the context's deadline), so a daemon hands it straight to
// lifecycle.Run.
type Server struct {
	httpSrv *http.Server
	ln      net.Listener
}

// New binds addr and returns a Server ready to Start, terminating TLS when cfg
// supplies a certificate and serving plaintext otherwise. Binding eagerly here
// surfaces an address-in-use error before the daemon's run loop begins.
func New(addr string, h http.Handler, cfg *config.Config) (*Server, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	if cfg.TLSEnabled() {
		tc, err := cfg.TLSConfig()
		if err != nil {
			ln.Close()
			return nil, err
		}
		ln = tls.NewListener(ln, tc)
	}
	return &Server{httpSrv: &http.Server{Handler: h}, ln: ln}, nil
}

// Addr reports the bound listen address, including the resolved port when addr
// requested an ephemeral one (":0").
func (s *Server) Addr() net.Addr { return s.ln.Addr() }

// Start serves until Shutdown is called; it returns http.ErrServerClosed on a
// graceful stop (the normal path) and closes the listener on return.
func (s *Server) Start() error { return s.httpSrv.Serve(s.ln) }

// Shutdown stops accepting new connections and drains in-flight requests, giving
// up when ctx's deadline passes.
func (s *Server) Shutdown(ctx context.Context) error { return s.httpSrv.Shutdown(ctx) }

// TLSListener binds addr and returns a listener that terminates TLS with the
// hardened config.TLSConfig — the implicit-TLS entry point for the mail daemons
// (IMAPS/POP3S/SMTPS), whose protocol servers accept the returned net.Listener
// directly. It errors if cfg has no certificate.
func TLSListener(addr string, cfg *config.Config) (net.Listener, error) {
	tc, err := cfg.TLSConfig()
	if err != nil {
		return nil, err
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	return tls.NewListener(ln, tc), nil
}
