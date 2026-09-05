package admin

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"hermex/internal/directory"
	"hermex/internal/logging"
	"hermex/internal/serve"
)

// pendingTTL is how long a half-finished panel login stays usable. It proves the
// password only, so it must not outlive the operator standing at the code prompt.
const pendingTTL = 5 * time.Minute

// errThrottled reports that the failed-attempt limiter refused the try. It is a
// distinct outcome from a wrong code, because the two need different answers:
// one is 429, the other 401.
var errThrottled = errors.New("admin: too many failed attempts")

// issuePendingSession mints the cookie for a password that was right for an
// operator who also carries a second factor. No session row is recorded, because
// the operator has not signed in yet.
func (s *Server) issuePendingSession(login string, uid int64) (session, csrf string) {
	session = signToken(s.secret, claims{
		Login:   login,
		UserID:  uid,
		Expiry:  time.Now().Add(pendingTTL).Unix(),
		Pending: true,
	})
	return session, newCSRFToken()
}

// pendingClaims returns a half-finished session's claims. Only the code prompt
// and the verification below call it; every other gate reads a pending token as
// no session at all.
func (s *Server) pendingClaims(r *http.Request) (claims, bool) {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return claims{}, false
	}
	cl, err := verifyToken(s.secret, c.Value)
	if err != nil || !cl.Pending {
		return claims{}, false
	}
	return cl, true
}

// secondFactorRequired reports whether the operator must clear a second factor.
// A directory that cannot answer refuses the login rather than admitting it,
// because the permissive answer here is the one that skips the factor entirely.
func (s *Server) secondFactorRequired(login string) (bool, error) {
	return directory.SecondFactorEnabled(s.dir, login)
}

// spendCode verifies a submitted code and reports whether it was accepted. It is
// throttled on the same two axes as the password, because six digits fall to a
// million tries.
func (s *Server) spendCode(r *http.Request, login, code string) (bool, error) {
	addr := serve.ClientAddr(r)
	if !s.limiter.Allowed(addr, login) {
		return false, errThrottled
	}
	ok, err := directory.SpendSecondFactor(s.dir, login, code, time.Now())
	if err != nil {
		return false, err
	}
	if !ok {
		s.limiter.Fail(addr, login)
		return false, nil
	}
	s.limiter.Succeed(addr, login)
	return true, nil
}

// handleSecondFactorVerify completes a pending API login.
func (s *Server) handleSecondFactorVerify(w http.ResponseWriter, r *http.Request) {
	cl, ok := s.pendingClaims(r)
	if !ok {
		http.Error(w, "no pending session", http.StatusUnauthorized)
		return
	}
	serve.SetUser(r, cl.Login)
	// The pending cookie carries its own CSRF token, so the code submission is
	// double-submit protected exactly like every other state-changing call.
	if !validCSRF(r) {
		http.Error(w, "missing or invalid CSRF token", http.StatusForbidden)
		return
	}
	var req struct {
		Code string `json:"code"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "malformed request", http.StatusBadRequest)
		return
	}
	accepted, err := s.spendCode(r, cl.Login, req.Code)
	switch {
	case err == errThrottled:
		http.Error(w, "too many failed attempts, try again later", http.StatusTooManyRequests)
		return
	case err != nil:
		s.logger.Emit(logging.Event{Level: logging.LevelError, Subsystem: logging.Admin,
			Name: "second.factor.verify.fail", User: cl.Login, Err: err.Error()})
		http.Error(w, "server error", http.StatusInternalServerError)
		return
	case !accepted:
		http.Error(w, "invalid code", http.StatusUnauthorized)
		return
	}
	session, csrf := s.issueSession(cl.Login, cl.UserID)
	setSessionCookies(w, session, csrf)
	writeJSON(w, map[string]any{"login": cl.Login, "csrfToken": csrf})
}

// handleUISecondFactorSubmit completes a pending panel login from the code form.
func (s *Server) handleUISecondFactorSubmit(w http.ResponseWriter, r *http.Request) {
	cl, ok := s.pendingClaims(r)
	if !ok {
		http.Redirect(w, r, "/admin/ui/login", http.StatusSeeOther)
		return
	}
	serve.SetUser(r, cl.Login)
	if !validFormCSRF(r) {
		s.renderCodeForm(w, r, http.StatusForbidden, "Your session expired. Sign in again.")
		return
	}
	accepted, err := s.spendCode(r, cl.Login, r.PostFormValue("code"))
	switch {
	case errors.Is(err, errThrottled):
		s.renderCodeForm(w, r, http.StatusTooManyRequests, "Too many failed attempts, try again later.")
		return
	case err != nil:
		s.logger.Emit(logging.Event{Level: logging.LevelError, Subsystem: logging.Admin,
			Name: "second.factor.verify.fail", User: cl.Login, Err: err.Error()})
		s.renderCodeForm(w, r, http.StatusInternalServerError, "Server error, please try again.")
		return
	case !accepted:
		s.renderCodeForm(w, r, http.StatusUnauthorized, "That code was not accepted.")
		return
	}
	session, csrf := s.issueSession(cl.Login, cl.UserID)
	setSessionCookies(w, session, csrf)
	http.Redirect(w, r, "/admin/ui/", http.StatusSeeOther)
}

// renderCodeForm draws the code prompt, carrying the CSRF token the pending
// cookie already holds so the form can be submitted.
func (s *Server) renderCodeForm(w http.ResponseWriter, r *http.Request, status int, errMsg string) {
	if status != http.StatusOK {
		w.WriteHeader(status)
	}
	s.render(w, "second_factor.html", map[string]any{"Error": errMsg, "CSRF": csrfCookieValue(r)})
}
