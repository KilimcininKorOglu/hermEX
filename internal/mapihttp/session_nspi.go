package mapihttp

import "sync"

// nspiSession is the server-side state an NSPI Bind establishes. NSPI is
// otherwise stateless — the STAT cursor is client-carried and the GAL row
// identity is deterministic — so the session holds only the authenticated user
// and the per-request sequence guard, with no ROP object table.
type nspiSession struct {
	user     string
	sequence string
}

// nspiSessionStore maps NSPI sid cookies to bound sessions. It mirrors the
// EMSMDB session store's sid+sequence scheme but carries no ROP/store state,
// since the address book keeps nothing per session.
type nspiSessionStore struct {
	mu sync.Mutex
	m  map[string]*nspiSession
}

func newNspiSessionStore() *nspiSessionStore {
	return &nspiSessionStore{m: make(map[string]*nspiSession)}
}

// bind mints an NSPI session for the user and returns its sid and initial
// sequence cookies.
func (s *nspiSessionStore) bind(user string) (sid, sequence string) {
	sid, sequence = newGUID(), newGUID()
	s.mu.Lock()
	s.m[sid] = &nspiSession{user: user, sequence: sequence}
	s.mu.Unlock()
	return sid, sequence
}

// validate checks a sequenced request's cookies and rolls the sequence in one
// atomic step (the same guard EMSMDB Execute uses): sid must resolve, belong to
// user, and seq must match. Bind, Unbind, and PING do not carry a sequence and
// do not call this. It returns the new sequence and rcSuccess, or the matching
// X-ResponseCode.
func (s *nspiSessionStore) validate(sid, seq, user string) (newSeq string, code int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.m[sid]
	if !ok {
		return "", rcInvalidCtxCookie
	}
	if c.user != user {
		return "", rcNoPriv
	}
	if c.sequence != seq {
		return "", rcInvalidSeq
	}
	c.sequence = newGUID()
	return c.sequence, rcSuccess
}

// drop discards a bound NSPI session (Unbind).
func (s *nspiSessionStore) drop(sid string) {
	s.mu.Lock()
	delete(s.m, sid)
	s.mu.Unlock()
}
