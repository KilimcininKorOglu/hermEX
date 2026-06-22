package rpchttp

import "testing"

// TestSessionCarriesRemoteAddr proves the virtual-connection session is seeded with
// the originating client address, the value the NSPI activity log relies on so a
// per-operation event records the real client (MS-RPCH "always set RemoteAddr").
// It also pins the create-once semantics: the address is fixed when the session is
// first created and a later request on the same connection does not re-seed it.
func TestSessionCarriesRemoteAddr(t *testing.T) {
	s := NewServer(Config{})

	vc := s.getOrCreate("vkey", "alice@hermex.test", "/mb/alice", "203.0.113.7:50000")
	if vc.sess.RemoteAddr != "203.0.113.7:50000" {
		t.Errorf("session RemoteAddr = %q, want the originating client address", vc.sess.RemoteAddr)
	}
	if vc.sess.User != "alice@hermex.test" {
		t.Errorf("session User = %q, want alice@hermex.test", vc.sess.User)
	}

	// A later request on the same virtual connection returns the same session; the
	// address set at creation sticks rather than being overwritten.
	vc2 := s.getOrCreate("vkey", "alice@hermex.test", "/mb/alice", "198.51.100.9:1")
	if vc2 != vc || vc2.sess.RemoteAddr != "203.0.113.7:50000" {
		t.Errorf("re-lookup RemoteAddr = %q, want the original creation address", vc2.sess.RemoteAddr)
	}
}
