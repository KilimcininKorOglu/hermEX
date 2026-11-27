package health

import (
	"testing"
	"time"
)

// TestComponentBoundsTheHeaderRead is the defect. Every daemon's own listener sets
// ReadHeaderTimeout, so a client that opens a connection and dribbles header bytes
// forever cannot hold a goroutine and a file descriptor. The health listener is a
// separate http.Server that was built without it, which is the one endpoint an
// operator most wants answering when a daemon is under load. Component's contract
// also bounds the read and write phases, since a probe is a small request and a
// small answer.
func TestComponentBoundsTheHeaderRead(t *testing.T) {
	c := Component(":0", Handler("test", "", time.Now()))

	if c.srv.ReadHeaderTimeout <= 0 {
		t.Fatal("the health listener has no ReadHeaderTimeout, so a slow client holds a connection indefinitely")
	}
	if c.srv.ReadTimeout <= 0 {
		t.Error("the health listener has no ReadTimeout, so a slow request body holds a connection indefinitely")
	}
	if c.srv.WriteTimeout <= 0 {
		t.Error("the health listener has no WriteTimeout, so a stalled reader holds a connection indefinitely")
	}
	if c.srv.IdleTimeout <= 0 {
		t.Error("the health listener has no IdleTimeout, so dead keep-alive connections are never reaped")
	}
}
