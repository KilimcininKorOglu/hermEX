package mapihttp

import (
	"crypto/rand"
	"fmt"
	"sync"
)

// sessionContext is the server-side state a MAPI/HTTP Connect establishes; the
// client holds only the opaque sid cookie that maps here. The per-Execute
// sequence cookie is an ordering/replay guard. (The ROP logon and object handle
// state is added when the ROP layer lands.)
type sessionContext struct {
	user     string
	mailbox  string
	sequence string
}

// sessionStore maps sid cookies to live session contexts. A mailbox is normally
// served by one client at a time, so a single mutex is sufficient.
type sessionStore struct {
	mu sync.Mutex
	m  map[string]*sessionContext
}

func newSessionStore() *sessionStore {
	return &sessionStore{m: make(map[string]*sessionContext)}
}

// create mints a session for the user and returns its sid and initial sequence.
func (s *sessionStore) create(user, mailbox string) (sid, sequence string) {
	sid, sequence = newGUID(), newGUID()
	s.mu.Lock()
	s.m[sid] = &sessionContext{user: user, mailbox: mailbox, sequence: sequence}
	s.mu.Unlock()
	return sid, sequence
}

// execute validates an Execute request against its session cookies in one
// atomic step: sid must resolve, the session must belong to user, and seq must
// match the current sequence. On success it rolls the sequence and returns the
// new value, the context, and rcSuccess; otherwise it returns the matching
// X-ResponseCode (invalid context cookie, no privilege, or invalid sequence).
func (s *sessionStore) execute(sid, seq, user string) (newSeq string, ctx *sessionContext, code int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, found := s.m[sid]
	if !found {
		return "", nil, rcInvalidCtxCookie
	}
	if c.user != user {
		return "", nil, rcNoPriv
	}
	if c.sequence != seq {
		return "", nil, rcInvalidSeq
	}
	c.sequence = newGUID()
	return c.sequence, c, rcSuccess
}

// drop discards a session (Disconnect).
func (s *sessionStore) drop(sid string) {
	s.mu.Lock()
	delete(s.m, sid)
	s.mu.Unlock()
}

// newGUID mints a random hyphenated GUID string. MAPI/HTTP cookies are opaque to
// the client, so any stable unique value is acceptable.
func newGUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
