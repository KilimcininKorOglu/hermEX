package relay

import (
	"bufio"
	"context"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

// legacySink is a mail exchanger that does not offer SMTPUTF8, which is what
// most of the deployed base still looks like. It records the command lines it
// read so a test can prove what was, or was not, put on the wire.
type legacySink struct {
	mu    sync.Mutex
	lines []string
}

func (s *legacySink) commands() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.lines...)
}

func (s *legacySink) record(line string) {
	s.mu.Lock()
	s.lines = append(s.lines, line)
	s.mu.Unlock()
}

// startLegacySink serves SMTP without the SMTPUTF8 capability and accepts
// everything else, so a refusal in the test can only come from the client side.
func startLegacySink(t *testing.T) (*legacySink, string) {
	t.Helper()
	sink := &legacySink{}
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
			go sink.serve(conn)
		}
	}()
	return sink, ln.Addr().String()
}

func (s *legacySink) serve(conn net.Conn) {
	defer conn.Close()
	br := bufio.NewReader(conn)
	write := func(reply string) { _, _ = conn.Write([]byte(reply)) }
	write("220 legacy.test ESMTP\r\n")
	inData := false
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		if inData {
			if line == "." {
				inData = false
				write("250 2.0.0 accepted\r\n")
			}
			continue
		}
		s.record(line)
		switch verb := strings.ToUpper(line); {
		case strings.HasPrefix(verb, "EHLO"):
			// No SMTPUTF8, deliberately.
			write("250-legacy.test\r\n250-PIPELINING\r\n250 8BITMIME\r\n")
		case strings.HasPrefix(verb, "DATA"):
			inData = true
			write("354 send it\r\n")
		case strings.HasPrefix(verb, "QUIT"):
			write("221 2.0.0 bye\r\n")
			return
		default:
			write("250 2.0.0 ok\r\n")
		}
	}
}

// TestWorkerRefusesAUTF8EnvelopeOnAHostWithoutSMTPUTF8 pins RFC 6531 §3.5: an
// internationalized message must not be sent to a mail exchanger that does not
// offer SMTPUTF8. net/smtp attaches the keyword whenever the server advertises
// it, but it never inspects the address, so without the guard the UTF-8 envelope
// goes out raw on a session that never negotiated it and the receiving side is
// free to read it as anything.
//
// The message stays queued rather than being consumed: the host is refused, not
// the message, so the next mail exchanger and the next pass still get a turn.
func TestWorkerRefusesAUTF8EnvelopeOnAHostWithoutSMTPUTF8(t *testing.T) {
	sink, addr := startLegacySink(t)
	sp := openSpool(t)
	t0 := time.Unix(3_000_000, 0)
	raw := []byte("From: alice@local\r\nSubject: out\r\n\r\nhi\r\n")
	if err := sp.Enqueue("alice@local", []string{"alıcı@örnek.test"}, raw, t0); err != nil {
		t.Fatal(err)
	}

	w := &Worker{
		Spool:    sp,
		HeloName: "mx.test",
		Router:   func(string) ([]string, error) { return []string{"legacy"}, nil },
		Dialer:   func(string) (net.Conn, error) { return net.Dial("tcp", addr) },
	}
	sent, err := w.ProcessDue(context.Background(), t0)
	if err != nil {
		t.Fatalf("process: %v", err)
	}
	if sent != 0 {
		t.Errorf("delivered %d messages to a host without SMTPUTF8, want 0", sent)
	}
	for _, cmd := range sink.commands() {
		if strings.HasPrefix(strings.ToUpper(cmd), "MAIL") {
			t.Errorf("a UTF-8 envelope reached a host that does not offer SMTPUTF8: %q", cmd)
		}
	}
}

// TestWorkerDeliversAUTF8EnvelopeToAnSMTPUTF8Host is the other half: the guard
// must refuse the host, not the address family. hermEX's own SMTP server offers
// SMTPUTF8, so the same message delivers there unchanged.
func TestWorkerDeliversAUTF8EnvelopeToAnSMTPUTF8Host(t *testing.T) {
	sink, addr := startSink(t)
	sp := openSpool(t)
	t0 := time.Unix(3_000_000, 0)
	const rcpt = "alıcı@örnek.test"
	raw := []byte("From: alice@local\r\nSubject: out\r\n\r\nhi\r\n")
	if err := sp.Enqueue("alice@local", []string{rcpt}, raw, t0); err != nil {
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
		t.Fatalf("delivered %d, want 1", sent)
	}
	msgs := sink.recorded()
	if len(msgs) != 1 || len(msgs[0].rcpt) != 1 || msgs[0].rcpt[0] != rcpt {
		t.Errorf("sink recorded %+v, want one message for %q", msgs, rcpt)
	}
}
