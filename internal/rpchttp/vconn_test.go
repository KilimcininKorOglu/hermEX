package rpchttp

import (
	"strconv"
	"testing"

	"hermex/internal/mapi"
)

// tableEntry reads the virtual connection the table currently holds for a key.
func tableEntry(s *Server, key string) *vconn {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.conns[key]
}

// TestAnotherUserCannotJoinAVirtualConnection opens channels for two accounts
// that present the same connection cookie. The cookie is chosen by the client
// ([MS-RPCH] 2.2.3.1), so it identifies a connection but authenticates nothing:
// a second account presenting it must get a connection of its own, never the
// first account's session, and cannot squat the first account's key either.
func TestAnotherUserCannotJoinAVirtualConnection(t *testing.T) {
	s := NewServer(Config{})
	cookie := mapi.GUID{Data1: 7}
	aliceKey := vconnKey(cookie, "mail.hermex.test", "6001", "alice@hermex.test")
	alice := s.getOrCreate(aliceKey, "alice@hermex.test", "/mb/alice", "198.51.100.1:1")
	bob := s.getOrCreate(vconnKey(cookie, "mail.hermex.test", "6001", "bob@hermex.test"), "bob@hermex.test", "/mb/bob", "198.51.100.2:1")
	if bob == alice {
		t.Fatal("a second account joined another account's virtual connection")
	}
	wantEq(t, bob.sess.User, "bob@hermex.test", "the second account's session user")
	wantEq(t, alice.sess.User, "alice@hermex.test", "the first account's session user")
	wantEq(t, tableEntry(s, aliceKey), alice, "alice table entry after bob lookup")
	wantEq(t, alice.sess.Mailbox, "/mb/alice", "alice mailbox after bob lookup")
	wantEq(t, alice.sess.RemoteAddr, "198.51.100.1:1", "alice remote addr after bob lookup")
	wantFalse(t, alice.isClosed(), "alice connection closed after bob lookup")
}

// TestTeardownLeavesAReplacementConnectionAlone is the load-bearing case. The IN and
// OUT channels of one virtual connection tear down independently, so a client can
// reconnect under the same cookie between the two teardowns. The late teardown must
// not remove the replacement.
func TestTeardownLeavesAReplacementConnectionAlone(t *testing.T) {
	s := NewServer(Config{})
	const key = "vkey"

	first := s.getOrCreate(key, "alice@hermex.test", "/mb/alice", "203.0.113.7:50000")
	s.teardown(key, first)

	second := s.getOrCreate(key, "alice@hermex.test", "/mb/alice", "203.0.113.7:50001")
	if second == first {
		t.Fatal("the replacement reused the torn-down connection")
	}

	// The other channel of the first connection now ends and tears down.
	s.teardown(key, first)

	wantEq(t, tableEntry(s, key), second, "the table entry after the late teardown")
	wantFalse(t, second.isClosed(), "the replacement connection is closed")
}

// TestTeardownClosesItsOwnConnection proves the caller's connection still goes away:
// the identity check narrows which table entry is removed, not whether the channel
// loops are unblocked.
func TestTeardownClosesItsOwnConnection(t *testing.T) {
	s := NewServer(Config{})
	const key = "vkey"

	vc := s.getOrCreate(key, "alice@hermex.test", "/mb/alice", "203.0.113.7:50000")
	s.teardown(key, vc)

	wantTrue(t, vc.isClosed(), "the torn-down connection is closed")
	if tableEntry(s, key) != nil {
		t.Error("the torn-down connection is still in the table")
	}
}

// TestGetOrCreateReplacesAClosedConnection proves a joining channel never takes a
// connection whose loops have already stopped, which would drop every PDU it queues.
func TestGetOrCreateReplacesAClosedConnection(t *testing.T) {
	s := NewServer(Config{})
	const key = "vkey"

	first := s.getOrCreate(key, "alice@hermex.test", "/mb/alice", "203.0.113.7:50000")
	first.close() // the peer channel closed it, but its teardown has not run yet

	second := s.getOrCreate(key, "alice@hermex.test", "/mb/alice", "203.0.113.7:50001")
	if second == first {
		t.Fatal("a joining channel was handed the closed connection")
	}
	wantFalse(t, second.isClosed(), "the replacement connection is closed")
	wantEq(t, tableEntry(s, key), second, "the table entry after the replacement")
}

// TestTableCapRefusesANewKeyButAllowsAReplacement proves the ceiling still bounds the
// table, and that replacing a closed entry is not refused by it, because such a
// replacement does not grow the table.
func TestTableCapRefusesANewKeyButAllowsAReplacement(t *testing.T) {
	s := NewServer(Config{})
	for i := range maxVirtualConnections {
		s.getOrCreate("key"+strconv.Itoa(i), "alice@hermex.test", "/mb/alice", "203.0.113.7:1")
	}

	if vc := s.getOrCreate("overflow", "alice@hermex.test", "/mb/alice", "203.0.113.7:1"); vc != nil {
		t.Error("a new key was admitted past the connection ceiling")
	}
	if tableEntry(s, "overflow") != nil {
		t.Error("the refused key holds a table entry")
	}
	s.mu.Lock()
	size := len(s.conns)
	s.mu.Unlock()
	wantEq(t, size, maxVirtualConnections, "table size after the refused key")

	const key = "key0"
	tableEntry(s, key).close()
	replacement := s.getOrCreate(key, "alice@hermex.test", "/mb/alice", "203.0.113.7:2")
	if replacement == nil {
		t.Fatal("replacing a closed entry was refused at the ceiling")
	}
	wantFalse(t, replacement.isClosed(), "the replacement connection is closed")
}

// TestTeardownNilAndMarkReadyGuards proves nil teardown does not drop a live
// connection and markReady triggers CONN/C2 exactly once when both channels join.
func TestTeardownNilAndMarkReadyGuards(t *testing.T) {
	s := NewServer(Config{})
	const key = "vkey"
	vc := s.getOrCreate(key, "alice@hermex.test", "/mb/alice", "203.0.113.7:50000")

	s.teardown(key, nil)
	wantEq(t, tableEntry(s, key), vc, "table entry after nil teardown")
	wantFalse(t, vc.isClosed(), "connection closed after nil teardown")

	wantFalse(t, vc.markReady("in"), "first in ready")
	wantFalse(t, vc.markReady("in"), "duplicate in ready")
	wantTrue(t, vc.markReady("out"), "out completes pair")
	wantFalse(t, vc.markReady("out"), "duplicate out after CONN/C2")
	wantFalse(t, vc.markReady("in"), "duplicate in after CONN/C2")
}
