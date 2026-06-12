package webmail

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"sync"
	"time"
)

const (
	sessionCookie = "hermex_session"
	sessionTTL    = 12 * time.Hour
)

// session is one authenticated webmail session. mailboxPath is the store path
// resolved at login, so requests open the user's mailbox without re-resolving.
type session struct {
	user        string
	mailboxPath string
	expires     time.Time
}

// sessionStore holds active sessions keyed by an unguessable random token.
type sessionStore struct {
	mu sync.Mutex
	m  map[string]*session
}

func newSessionStore() *sessionStore {
	return &sessionStore{m: make(map[string]*session)}
}

// create registers a new session and returns its token.
func (s *sessionStore) create(user, mailboxPath string) string {
	token := randomToken()
	s.mu.Lock()
	s.m[token] = &session{user: user, mailboxPath: mailboxPath, expires: time.Now().Add(sessionTTL)}
	s.mu.Unlock()
	return token
}

// get returns the session for a token if it exists and has not expired.
func (s *sessionStore) get(token string) (*session, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.m[token]
	if !ok {
		return nil, false
	}
	if time.Now().After(sess.expires) {
		delete(s.m, token)
		return nil, false
	}
	return sess, true
}

// destroy removes a session.
func (s *sessionStore) destroy(token string) {
	s.mu.Lock()
	delete(s.m, token)
	s.mu.Unlock()
}

// randomToken returns a 256-bit cryptographically random hex token.
func randomToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic("webmail: crypto/rand failed: " + err.Error())
	}
	return hex.EncodeToString(b)
}

// sessionFrom returns the live session for a request, or ok=false when the
// request carries no valid session cookie.
func (s *Server) sessionFrom(r *http.Request) (*session, bool) {
	cookie, err := r.Cookie(sessionCookie)
	if err != nil {
		return nil, false
	}
	return s.sessions.get(cookie.Value)
}
