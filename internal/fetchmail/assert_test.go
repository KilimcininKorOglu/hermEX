package fetchmail

import (
	"errors"
	"io"
	"net"
	"strings"
	"testing"
)

// scriptWriter writes a scripted server's replies and keeps the first write
// failure, so the session reports it instead of carrying on against a dead link.
type scriptWriter struct {
	w   io.Writer
	err error
}

func (s *scriptWriter) send(text string) {
	if s.err == nil {
		_, s.err = io.WriteString(s.w, text)
	}
}

// serveOne accepts one connection on ln and runs session on it; session returns
// the connection it ended on (a STARTTLS upgrade replaces the first) and the
// failure that ended it, nil for a clean end. Cleanup closes the listener and
// requires the session to have ended cleanly: a listener closed before any
// client connected ends the accept with net.ErrClosed, which is a clean end.
func serveOne(t *testing.T, ln net.Listener, session func(net.Conn) (net.Conn, error)) {
	t.Helper()
	done := make(chan error, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			done <- err
			return
		}
		last, err := session(conn)
		done <- errors.Join(err, last.Close())
	}()
	t.Cleanup(func() {
		if err := ln.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			t.Errorf("close the scripted listener: %v", err)
		}
		if err := <-done; err != nil && !errors.Is(err, net.ErrClosed) {
			t.Errorf("scripted server: %v", err)
		}
	})
}

// readEnded reports whether a scripted server's read failed because the client
// ended the session, which is the normal end of a script.
func readEnded(err error) bool {
	return errors.Is(err, io.EOF)
}

// This file is the package's assertion vocabulary. A test states one fact per call,
// so a failure names the fact that broke rather than the condition that evaluated.

// wantEq fails the test unless the value equals what the caller expected.
func wantEq[T comparable](t *testing.T, got, want T, what string) {
	t.Helper()
	if got != want {
		t.Errorf("%s = %v, want %v", what, got, want)
	}
}

// mustNoErr stops the test on an error from a step under test.
func mustNoErr(t *testing.T, err error, what string) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s: %v", what, err)
	}
}

// wantRecorded fails the test unless the scripted server recorded the command.
func wantRecorded(t *testing.T, recorded []string, sub, what string) {
	t.Helper()
	joined := strings.Join(recorded, " | ")
	if !strings.Contains(joined, sub) {
		t.Errorf("%s: %q missing from the recorded commands %q", what, sub, joined)
	}
}
