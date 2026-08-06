package mapihttp

import (
	"crypto/rand"
	"fmt"
	"sync"
	"time"

	"hermex/internal/directory"
	"hermex/internal/logging"
	"hermex/internal/relay"
	"hermex/internal/rop"
)

// sessionContext is the server-side state a MAPI/HTTP Connect establishes; the
// client holds only the opaque sid cookie that maps here. The per-Execute
// sequence cookie is an ordering/replay guard. ropSess holds the ROP object and
// handle table, which lives across Execute calls until Disconnect. lastSeen is
// the last time the client touched the session and created is when Connect minted
// it; both are guarded by the store's mutex, not read directly.
type sessionContext struct {
	user     string
	mailbox  string
	sequence string
	ropSess  *rop.Session
	lastSeen time.Time
	created  time.Time
}

// sessionStore maps sid cookies to live session contexts. A mailbox is normally
// served by one client at a time, so a single mutex is sufficient.
//
// maxAge is the absolute lifetime: a session is refused and reclaimed once it
// reaches that age however busy it has been. Idle reclamation alone never
// reaches a client that keeps polling, and such a session pins a ROP object and
// handle table with an open mailbox store for as long as the client runs, so
// both the table and the handles it accumulates grow without bound. Reaching the
// cap answers the next request with the invalid-context code, which is the
// re-Connect signal [MS-OXCMAPIHTTP] already defines, so a live client rebuilds
// its session rather than failing. A maxAge of zero disables the cap.
type sessionStore struct {
	mu     sync.Mutex
	m      map[string]*sessionContext
	maxAge time.Duration
}

func newSessionStore(maxAge time.Duration) *sessionStore {
	return &sessionStore{m: make(map[string]*sessionContext), maxAge: maxAge}
}

// tooOld reports whether a session has reached its absolute lifetime. The caller
// holds the store's mutex.
func (s *sessionStore) tooOld(c *sessionContext, now time.Time) bool {
	return s.maxAge > 0 && now.Sub(c.created) >= s.maxAge
}

// create mints a session for the user and returns its sid and initial sequence.
// accounts is the recipient directory the session's ROP layer resolves against
// when submitting mail; the authenticated user doubles as the session owner's
// SMTP address (the From of a submitted message).
func (s *sessionStore) create(user, mailbox string, accounts directory.Accounts, spool *relay.Spool, logger *logging.Logger) (sid, sequence string) {
	sid, sequence = newSessionToken(), newSessionToken()
	now := time.Now()
	s.mu.Lock()
	s.m[sid] = &sessionContext{
		user:     user,
		mailbox:  mailbox,
		sequence: sequence,
		ropSess:  rop.NewSession(mailbox, accounts, user, rop.WithSpool(spool), rop.WithLogger(logger)),
		lastSeen: now,
		created:  now,
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
	now := time.Now()
	c, found := s.m[sid]
	if !found || s.tooOld(c, now) {
		return "", nil, rcInvalidCtxCookie
	}
	if c.user != user {
		return "", nil, rcNoPriv
	}
	if c.sequence != seq {
		return "", nil, rcInvalidSeq
	}
	c.sequence = newSessionToken()
	c.lastSeen = now
	return c.sequence, c, rcSuccess
}

// lookup resolves a session by its sid cookie without rolling the sequence, for
// NotificationWait, which runs on a parallel connection outside the Execute
// sequence. It returns nil when the sid is unknown, when the session has reached
// its absolute lifetime, or when it belongs to another account: the sid alone is
// not authority over a session, and without the owner check any authenticated
// caller holding another user's sid could park a wait on that mailbox and learn
// when it changes.
func (s *sessionStore) lookup(sid, user string) *sessionContext {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.m[sid]
	if c == nil || c.user != user || s.tooOld(c, time.Now()) {
		return nil
	}
	// Stamp at the start of the long poll, not its end: a wait that parks for
	// most of a minute must not look idle while it is running.
	c.lastSeen = time.Now()
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

// sweep discards every session idle for longer than ttl, and every session past
// the store's absolute lifetime however recently it was used, returning how many
// it reclaimed. A client that dies without Disconnect leaves its session behind,
// and each one pins a ROP handle table, so without this the server accumulates
// open mailbox stores for clients that are never coming back. ROP tables are
// closed outside the lock, as drop does.
func (s *sessionStore) sweep(now time.Time, ttl time.Duration) int {
	var expired []*sessionContext
	s.mu.Lock()
	for sid, c := range s.m {
		if now.Sub(c.lastSeen) >= ttl || s.tooOld(c, now) {
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
