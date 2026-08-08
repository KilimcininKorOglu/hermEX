// Package avtest starts a stub clamd for tests. The antivirus client speaks to a
// real socket, so a test that needs to prove a protocol refuses infected content
// needs a server to answer it; every package that stores client-supplied content
// needs the same one, which is why it lives here rather than in each of them.
//
// It is test support only, the same role internal/tlstest plays for certificates.
package avtest

import (
	"encoding/binary"
	"io"
	"net"
	"testing"
)

// Verdicts a stub can answer with. They are the clamd INSTREAM reply lines.
const (
	Clean    = "stream: OK\x00"
	Infected = "stream: Eicar-Test-Signature FOUND\x00"
)

// Clamd starts a stub clamd answering every INSTREAM with reply, and returns the
// address to configure a scanner with. It stops when the test ends.
func Clamd(t *testing.T, reply string) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go serve(c, reply)
		}
	}()
	return "tcp://" + ln.Addr().String()
}

// serve drains one INSTREAM (the command line, then length-prefixed chunks up to
// the zero-length terminator) and answers with reply.
func serve(c net.Conn, reply string) {
	defer c.Close()
	cmd := make([]byte, len("zINSTREAM\x00"))
	if _, err := io.ReadFull(c, cmd); err != nil {
		return
	}
	var hdr [4]byte
	for {
		if _, err := io.ReadFull(c, hdr[:]); err != nil {
			return
		}
		n := binary.BigEndian.Uint32(hdr[:])
		if n == 0 {
			break
		}
		if _, err := io.CopyN(io.Discard, c, int64(n)); err != nil {
			return
		}
	}
	_, _ = io.WriteString(c, reply)
}
