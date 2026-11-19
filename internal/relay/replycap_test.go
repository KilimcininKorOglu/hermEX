package relay

import (
	"bytes"
	"net"
	"sync/atomic"
	"testing"
	"time"
)

// TestAnEndlessReplyLineIsCut connects to a mail exchanger that answers with a
// greeting line that never ends. The exchanger is chosen by the recipient's
// domain, so it is hostile input; RFC 5321 4.5.3.1.5 bounds a reply line at 512
// octets, and Postfix keeps its client's memory bounded for the same reason
// ("someone sends infinitely-long requests or replies"). The send must fail
// after a bounded read instead of buffering the line until the session timeout.
func TestAnEndlessReplyLineIsCut(t *testing.T) {
	client, server := net.Pipe()
	var sent atomic.Int64
	go func() {
		defer server.Close()
		chunk := bytes.Repeat([]byte("a"), 32<<10)
		if _, err := server.Write([]byte("220 ")); err != nil {
			return
		}
		for sent.Load() < 64<<20 {
			n, err := server.Write(chunk)
			sent.Add(int64(n))
			if err != nil {
				return
			}
		}
	}()

	w := &Worker{Dialer: func(string) (net.Conn, error) { return client, nil }}
	done := make(chan error, 1)
	go func() { done <- w.send("mx.hostile.test", Item{}, false) }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("an endless greeting line was accepted")
		}
	case <-time.After(30 * time.Second):
		t.Fatal("send kept reading an endless reply line")
	}
	if n := sent.Load(); n > 2*maxServerTurnBytes {
		t.Errorf("read %d bytes of one reply, want at most about %d", n, maxServerTurnBytes)
	}
}
