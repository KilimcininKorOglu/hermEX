// Package avtest starts a stub clamd for tests. The antivirus client speaks to a
// real socket, so a test that needs to prove a protocol refuses infected content
// needs a server to answer it; every package that stores client-supplied content
// needs the same one, which is why it lives here rather than in each of them.
//
// It is test support only, the same role internal/tlstest plays for certificates.
package avtest

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"testing"
)

// Verdicts a stub can answer with. They are the clamd INSTREAM reply lines.
const (
	Clean    = "stream: OK\x00"
	Infected = "stream: Eicar-Test-Signature FOUND\x00"
)

// Clamd starts a stub clamd answering every INSTREAM with reply, and returns the
// address to configure a scanner with. It stops when the test ends; a verdict it
// could not send, or a connection it could not close, fails the test then, so a
// scan result never rests on a stub that silently stopped answering.
func Clamd(t *testing.T, reply string) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		errs []error
	)
	t.Cleanup(func() {
		if err := ln.Close(); err != nil {
			t.Errorf("avtest: close listener: %v", err)
		}
		wg.Wait()
		mu.Lock()
		defer mu.Unlock()
		if err := errors.Join(errs...); err != nil {
			t.Errorf("avtest: %v", err)
		}
	})
	wg.Go(func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return // the listener closed at cleanup
			}
			wg.Go(func() {
				if err := serve(c, reply); err != nil {
					mu.Lock()
					errs = append(errs, err)
					mu.Unlock()
				}
			})
		}
	})
	return "tcp://" + ln.Addr().String()
}

// serve drains one INSTREAM (the command line, then length-prefixed chunks up to
// the zero-length terminator) and answers with reply. A client that hangs up
// before the terminator gets no verdict, which is not the stub's failure; a
// verdict that cannot be written, or a close that fails, is.
func serve(c net.Conn, reply string) (err error) {
	defer func() {
		if cerr := c.Close(); cerr != nil {
			err = errors.Join(err, fmt.Errorf("close connection: %w", cerr))
		}
	}()
	cmd := make([]byte, len("zINSTREAM\x00"))
	if _, err := io.ReadFull(c, cmd); err != nil {
		return nil
	}
	var hdr [4]byte
	for {
		if _, err := io.ReadFull(c, hdr[:]); err != nil {
			return nil
		}
		n := binary.BigEndian.Uint32(hdr[:])
		if n == 0 {
			break
		}
		if _, err := io.CopyN(io.Discard, c, int64(n)); err != nil {
			return nil
		}
	}
	if _, err := io.WriteString(c, reply); err != nil {
		return fmt.Errorf("write verdict: %w", err)
	}
	return nil
}
