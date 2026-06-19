package mapihttp

import (
	"crypto/rand"
	"fmt"
	"sync"

	"hermex/internal/directory"
	"hermex/internal/relay"
	"hermex/internal/rop"
)

// sessionContext is the server-side state a MAPI/HTTP Connect establishes; the
// client holds only the opaque sid cookie that maps here. The per-Execute
// sequence cookie is an ordering/replay guard. ropSess holds the ROP object and
// handle table, which lives across Execute calls until Disconnect.
type sessionContext struct {
	user     string
	mailbox  string
	sequence string
	ropSess  *rop.Session
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
// accounts is the recipient directory the session's ROP layer resolves against
// when submitting mail; the authenticated user doubles as the session owner's
// SMTP address (the From of a submitted message).
func (s *sessionStore) create(user, mailbox string, accounts directory.Accounts, spool *relay.Spool) (sid, sequence string) {
	sid, sequence = newGUID(), newGUID()
	s.mu.Lock()
	s.m[sid] = &sessionContext{user: user, mailbox: mailbox, sequence: sequence, ropSess: rop.NewSession(mailbox, accounts, user, rop.WithSpool(spool))}
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

// lookup resolves a session by its sid cookie without rolling the sequence — for
// NotificationWait, which runs on a parallel connection outside the Execute
// sequence. It returns nil when the sid is unknown.
func (s *sessionStore) lookup(sid string) *sessionContext {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.m[sid]
}

// drop discards a session (Disconnect), closing its ROP object table (and any
// open store) outside the lock.
func (s *sessionStore) drop(sid string) {
	s.mu.Lock()
	c := s.m[sid]
	delete(s.m, sid)
	s.mu.Unlock()
	if c != nil && c.ropSess != nil {
		c.ropSess.Close()
	}
}

// newGUID mints a random hyphenated GUID string. MAPI/HTTP cookies are opaque to
// the client, so any stable unique value is acceptable.
func newGUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
