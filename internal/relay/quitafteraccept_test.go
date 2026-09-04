package relay

import (
	"bufio"
	"context"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// serveHangupSession speaks just enough SMTP to accept one message, then drops
// the connection the moment it has answered the end-of-data dot, without ever
// replying to QUIT. Many production mail exchangers behave exactly this way.
func serveHangupSession(conn net.Conn, accepted *int32) {
	defer func() { _ = conn.Close() }()
	r := bufio.NewReader(conn)
	write := func(s string) { _, _ = conn.Write([]byte(s + "\r\n")) }

	write("220 sink.test ESMTP")
	inData := false
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		if inData {
			if strings.TrimRight(line, "\r\n") == "." {
				atomic.AddInt32(accepted, 1)
				write("250 2.0.0 accepted")
				return // hang up on an accepted message, before QUIT
			}
			continue
		}
		cmd := strings.ToUpper(strings.TrimRight(line, "\r\n"))
		switch {
		case strings.HasPrefix(cmd, "EHLO"), strings.HasPrefix(cmd, "HELO"):
			write("250 sink.test")
		case strings.HasPrefix(cmd, "DATA"):
			write("354 go ahead")
			inData = true
		default:
			write("250 2.0.0 ok")
		}
	}
}

// startHangupSink runs the above mail exchanger and reports how many messages it
// has accepted.
func startHangupSink(t *testing.T) (*int32, string) {
	t.Helper()
	accepted := new(int32)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go serveHangupSession(conn, accepted)
		}
	}()
	return accepted, ln.Addr().String()
}

// TestAcceptedMessageSurvivesFailedQuit is the duplicate-delivery regression. The
// mail exchanger accepts the message and then hangs up before answering QUIT, so
// QUIT errors. That error used to be returned as a delivery failure, which left
// the recipient unsettled and re-sent an already-accepted message on the next
// pass. Acceptance is established at the end-of-data reply, so the pass must
// settle and the spool must drain.
func TestAcceptedMessageSurvivesFailedQuit(t *testing.T) {
	accepted, addr := startHangupSink(t)
	sp := openSpool(t)
	t0 := time.Unix(3_000_000, 0)
	raw := []byte("From: alice@local\r\nSubject: out\r\n\r\nhi bob\r\n")
	if err := sp.Enqueue("alice@local", []string{"bob@remote"}, raw, t0); err != nil {
		t.Fatal(err)
	}

	w := &Worker{
		Spool:    sp,
		HeloName: "mx.test",
		Router:   func(string) ([]string, error) { return []string{"sink"}, nil },
		Dialer:   func(string) (net.Conn, error) { return net.Dial("tcp", addr) },
	}
	sent, err := w.ProcessDue(context.Background(), t0)
	if err != nil {
		t.Fatalf("process: %v", err)
	}
	if sent != 1 {
		t.Fatalf("settled %d recipients, want 1; the accepted message was reported as failed", sent)
	}
	if n := atomic.LoadInt32(accepted); n != 1 {
		t.Errorf("the mail exchanger accepted %d copies, want 1", n)
	}
	if due, _ := sp.Claim(t0, 10); len(due) != 0 {
		t.Errorf("spool still holds %d recipients, so the message would be sent again", len(due))
	}
}
