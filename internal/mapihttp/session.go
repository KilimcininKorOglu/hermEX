package mapihttp

import (
	"crypto/rand"
	"fmt"
	"sync"
	"time"

	"hermex/internal/directory"
	"hermex/internal/relay"
	"hermex/internal/rop"
)

// sessionContext is the server-side state a MAPI/HTTP Connect establishes; the
// client holds only the opaque sid cookie that maps here. The per-Execute
// sequence cookie is an ordering/replay guard. ropSess holds the ROP object and
// handle table, which lives across Execute calls until Disconnect. lastSeen is
// the last time the client touched the session, so one that stops answering can
// be reclaimed; it is guarded by the store's mutex, not read directly.
type sessionContext struct {
	user     string
	mailbox  string
	sequence string
	ropSess  *rop.Session
	lastSeen time.Time
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
	sid, sequence = newSessionToken(), newSessionToken()
	s.mu.Lock()
	s.m[sid] = &sessionContext{
		user:     user,
		mailbox:  mailbox,
		sequence: sequence,
		ropSess:  rop.NewSession(mailbox, accounts, user, rop.WithSpool(spool)),
		lastSeen: time.Now(),
	}
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
	c.sequence = newSessionToken()
	c.lastSeen = time.Now()
	return c.sequence, c, rcSuccess
}

// lookup resolves a session by its sid cookie without rolling the sequence — for
// NotificationWait, which runs on a parallel connection outside the Execute
// sequence. It returns nil when the sid is unknown.
func (s *sessionStore) lookup(sid string) *sessionContext {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.m[sid]
	if c != nil {
		// Stamp at the start of the long poll, not its end: a wait that parks for
		// most of a minute must not look idle while it is running.
		c.lastSeen = time.Now()
	}
	return c
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

// sweep discards every session idle for longer than ttl and returns how many it
// reclaimed. A client that dies without Disconnect leaves its session behind, and
// each one pins a ROP handle table, so without this the server accumulates open
// mailbox stores for clients that are never coming back. ROP tables are closed
// outside the lock, as drop does.
func (s *sessionStore) sweep(now time.Time, ttl time.Duration) int {
	var expired []*sessionContext
	s.mu.Lock()
	for sid, c := range s.m {
		if now.Sub(c.lastSeen) >= ttl {
			delete(s.m, sid)
			expired = append(expired, c)
		}
	}
	s.mu.Unlock()
	for _, c := range expired {
		if c.ropSess != nil {
			c.ropSess.Close()
		}
	}
	return len(expired)
}

// newGUID mints a random hyphenated GUID string, used where a GUID shape is
// required (e.g. the server GUID). Session cookies use newSessionToken instead.
func newGUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// newSessionToken mints a 256-bit random token as a 64-character hex string, used
// for the opaque sid and sequence cookies. It carries more entropy than a 16-byte
// GUID and no discernible internal structure, matching the webmail session-token
// approach (OWASP session-id guidance).
func newSessionToken() string {
	var b [32]byte
	_, _ = rand.Read(b[:])
	return fmt.Sprintf("%x", b[:])
}
