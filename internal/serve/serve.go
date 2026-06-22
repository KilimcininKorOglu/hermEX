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

	"hermex/internal/logging"
)

// TLSSource supplies the TLS decision and configuration to a listener: whether to
// terminate TLS at all, and the hardened tls.Config to do it with. *config.Config
// satisfies it (a single static certificate); *tlscert.Provider also satisfies it
// (a poll-refreshed, SNI-resolved certificate set), so a daemon gains live
// certificate reload by passing the provider here in place of the config.
type TLSSource interface {
	TLSEnabled() bool
	TLSConfig() (*tls.Config, error)
}

// Server is a bound HTTP server ready to start and shut down gracefully. It
// satisfies lifecycle.Component (Start blocks serving; Shutdown drains in-flight
// requests within the context's deadline), so a daemon hands it straight to
// lifecycle.Run.
type Server struct {
	httpSrv *http.Server
	ln      net.Listener
}

// New binds addr and returns a Server ready to Start, terminating TLS when tls
// supplies a certificate and serving plaintext otherwise. Binding eagerly here
// surfaces an address-in-use error before the daemon's run loop begins. Every
// request is logged through logger under subsystem (method/path/status/duration/
// client/user/request-id); a nil logger disables request logging.
func New(addr string, h http.Handler, tlsSrc TLSSource, logger *logging.Logger, subsystem logging.Subsystem) (*Server, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	if tlsSrc.TLSEnabled() {
		tc, err := tlsSrc.TLSConfig()
		if err != nil {
			ln.Close()
			return nil, err
		}
		if logger != nil {
			// Log each completed TLS handshake (version + cipher) under the tls
			// subsystem. VerifyConnection runs once per connection after the normal
			// verification; returning nil leaves that verification unchanged.
			tc = tc.Clone()
			tc.VerifyConnection = func(cs tls.ConnectionState) error {
				logger.Emit(logging.Event{
					Level:     logging.LevelInfo,
					Subsystem: logging.TLS,
					Name:      "handshake",
					Fields:    logging.Fields{"version": tlsVersionName(cs.Version), "cipher": tls.CipherSuiteName(cs.CipherSuite)},
				})
				return nil
			}
		}
		ln = tls.NewListener(ln, tc)
	}
	return &Server{httpSrv: &http.Server{Handler: logMiddleware(h, logger, subsystem)}, ln: ln}, nil
}

// tlsVersionName renders a TLS version constant as a human-readable number for the
// handshake log.
func tlsVersionName(v uint16) string {
	switch v {
	case tls.VersionTLS13:
		return "1.3"
	case tls.VersionTLS12:
		return "1.2"
	case tls.VersionTLS11:
		return "1.1"
	case tls.VersionTLS10:
		return "1.0"
	default:
		return "unknown"
	}
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
// hardened tls.Config from tlsSrc — the implicit-TLS entry point for the mail
// daemons (IMAPS/POP3S/SMTPS), whose protocol servers accept the returned
// net.Listener directly. It errors if tlsSrc has no certificate.
func TLSListener(addr string, tlsSrc TLSSource) (net.Listener, error) {
	tc, err := tlsSrc.TLSConfig()
	if err != nil {
		return nil, err
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	return tls.NewListener(ln, tc), nil
}
