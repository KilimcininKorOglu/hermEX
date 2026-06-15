// Package serve is the shared HTTP serving entrypoint for the hermEX daemons. It
// terminates TLS when the configuration supplies a certificate and falls back to
// plaintext otherwise, so a daemon gains HTTPS by configuration alone — without
// each command duplicating the TLS-versus-plaintext decision or the hardened
// config.TLSConfig (TLS 1.2 floor).
package serve

import (
	"crypto/tls"
	"net"
	"net/http"

	"hermex/internal/config"
)

// ListenAndServe binds addr and serves h, terminating TLS when cfg supplies a
// certificate and serving plaintext otherwise. It blocks until the listener is
// closed or the bind fails.
func ListenAndServe(addr string, h http.Handler, cfg *config.Config) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	return Serve(ln, h, cfg)
}

// Serve serves h on ln. When cfg supplies a certificate it wraps ln in a TLS
// listener built from the hardened config.TLSConfig; otherwise it serves
// plaintext. It blocks until ln is closed and closes ln on return.
func Serve(ln net.Listener, h http.Handler, cfg *config.Config) error {
	if cfg.TLSEnabled() {
		tc, err := cfg.TLSConfig()
		if err != nil {
			return err
		}
		ln = tls.NewListener(ln, tc)
	}
	return (&http.Server{Handler: h}).Serve(ln)
}

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
