package antivirus

import (
	"fmt"
	"io"
	"net"
	"strings"
	"time"
)

// DefaultTimeout bounds a scan's dial and I/O when the caller sets none.
const DefaultTimeout = 30 * time.Second

// Result is the outcome of scanning one message.
type Result struct {
	Clean     bool   // true when clamd reported no signature match
	VirusName string // the signature name when Clean is false
}

// Scanner streams message bytes to a clamd daemon over INSTREAM. The zero value
// is not usable; construct one with New. It is safe for concurrent use: each
// Scan dials a fresh connection.
type Scanner struct {
	network string        // "tcp" or "unix"
	address string        // dial target
	timeout time.Duration // per-connection dial + I/O deadline; 0 disables
}

// New builds a Scanner for a clamd address. Accepted forms: "tcp://host:port",
// "unix:///path/to/clamd.sock", or a bare "host:port" (assumed TCP). The address
// is not dialed here; connection errors surface from Scan.
func New(addr string) (*Scanner, error) {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return nil, fmt.Errorf("antivirus: empty clamd address")
	}
	s := &Scanner{timeout: DefaultTimeout}
	switch {
	case strings.HasPrefix(addr, "unix://"):
		s.network, s.address = "unix", strings.TrimPrefix(addr, "unix://")
	case strings.HasPrefix(addr, "tcp://"):
		s.network, s.address = "tcp", strings.TrimPrefix(addr, "tcp://")
	default:
		s.network, s.address = "tcp", addr
	}
	if s.address == "" {
		return nil, fmt.Errorf("antivirus: clamd address %q has no target", addr)
	}
	return s, nil
}

// Scan streams raw to clamd and reports whether it matched a signature. A non-nil
// error means the scan did not complete (dial, I/O, or a clamd ERROR reply) and
// the caller applies its fail policy. A completed scan returns a Result with a
// nil error whether or not a virus was found.
func (s *Scanner) Scan(raw []byte) (Result, error) {
	conn, err := net.DialTimeout(s.network, s.address, s.timeout)
	if err != nil {
		return Result{}, fmt.Errorf("antivirus: dial clamd: %w", err)
	}
	defer conn.Close()
	if s.timeout > 0 {
		// One deadline covers the whole exchange; clamd's own ReadTimeout also applies.
		_ = conn.SetDeadline(time.Now().Add(s.timeout))
	}

	if err := writeStream(conn, raw); err != nil {
		return Result{}, fmt.Errorf("antivirus: stream to clamd: %w", err)
	}

	reply, err := io.ReadAll(conn)
	if err != nil {
		return Result{}, fmt.Errorf("antivirus: read clamd reply: %w", err)
	}
	return parseReply(reply)
}

// Ping verifies clamd is reachable and responding (zPING -> PONG). It is a health
// probe, not part of scanning.
func (s *Scanner) Ping() error {
	conn, err := net.DialTimeout(s.network, s.address, s.timeout)
	if err != nil {
		return fmt.Errorf("antivirus: dial clamd: %w", err)
	}
	defer conn.Close()
	if s.timeout > 0 {
		_ = conn.SetDeadline(time.Now().Add(s.timeout))
	}
	if _, err := io.WriteString(conn, "zPING\x00"); err != nil {
		return fmt.Errorf("antivirus: ping clamd: %w", err)
	}
	reply, err := io.ReadAll(conn)
	if err != nil {
		return fmt.Errorf("antivirus: read ping reply: %w", err)
	}
	got := strings.Trim(string(reply), "\x00\n\r ")
	if !strings.HasPrefix(got, "PONG") {
		return fmt.Errorf("antivirus: unexpected ping reply %q", got)
	}
	return nil
}
