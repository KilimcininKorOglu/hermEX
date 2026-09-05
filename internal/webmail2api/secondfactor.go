package webmail2api

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"hermex/internal/directory"
	"hermex/internal/logging"
	"hermex/internal/serve"
	"hermex/internal/totp"
)

// pendingTTL is how long a half-finished login stays usable. It is short on
// purpose: the token proves the password only, so it must not outlive the user
// standing at the code prompt.
const pendingTTL = 5 * time.Minute

// totpSkew is how many steps either side of now a code may come from, which
// absorbs the clock drift of the user's phone. One step is thirty seconds.
const totpSkew = 1

// secondFactor returns the directory's second-factor capability. A directory
// that does not implement it has no second factor at all, so every caller
// treats ok=false as "nobody is enrolled" rather than as a failure.
func (s *Server) secondFactor() (directory.SecondFactorStore, bool) {
	sf, ok := s.auth.(directory.SecondFactorStore)
	return sf, ok
}

// secondFactorEnabled reports whether the account must clear a second factor.
// A read that fails is reported to the caller rather than swallowed, because
// swallowing it would answer "no second factor" and let the login through: the
// fallback here is the LESS restrictive one.
func (s *Server) secondFactorEnabled(user string) (bool, error) {
	sf, ok := s.secondFactor()
	if !ok {
		return false, nil
	}
	e, found, err := sf.TOTPEnrollment(user)
	if err != nil {
		return false, err
	}
	return found && e.Enabled, nil
}

// pendingAllowed reports whether a path stays reachable while the session has
// cleared the password but not the second factor: the auth probes, the code
// submission, and logging out.
func pendingAllowed(path string) bool {
	switch path {
	case "/api/v1/auth/login", "/api/v1/auth/logout", "/api/v1/auth/me", "/api/v1/auth/2fa/verify":
		return true
	}
	return false
}

// gateSecondFactor refuses every API call except the allowlist while the session
// is still pending. Without it the SPA's redirect to the code prompt would be
// cosmetic: the pending cookie is a real session cookie, so it could call the
// data API directly and the second factor would protect nothing.
func (s *Server) gateSecondFactor(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/v1/") && !pendingAllowed(r.URL.Path) {
			if c, ok := s.session(r); ok && c.Pending {
				writeJSON(w, http.StatusForbidden, map[string]string{"error": "second factor required"})
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// handleSecondFactorVerify completes a pending login. It accepts either a code
// from the authenticator or one of the recovery codes, and mints the real
// session only once one of them is spent.
func (s *Server) handleSecondFactorVerify(w http.ResponseWriter, r *http.Request) {
	c, ok := s.session(r)
	if !ok || !c.Pending {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	serve.SetUser(r, c.Email)
	var req struct {
		Code string `json:"code"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad request"})
		return
	}
	// A six-digit code is guessable in a million tries, so this endpoint is
	// throttled on the same two axes as the password: without it the second
	// factor would be weaker than the password it backs.
	addr := serve.ClientAddr(r)
	if !s.limiter.Allowed(addr, c.Email) {
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "too many failed attempts, try again later"})
		return
	}
	accepted, err := s.spendSecondFactor(c.Email, req.Code)
	if err != nil {
		logError("second-factor-verify", err, logging.Fields{"user": c.Email})
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	if !accepted {
		s.limiter.Fail(addr, c.Email)
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid code"})
		return
	}
	s.limiter.Succeed(addr, c.Email)
	s.issueSession(w, r, c.Email, c.Mailbox)
}

// spendSecondFactor accepts a code from the authenticator or a recovery code and
// reports whether one was spent. The authenticator is tried first, so a typo
// there never burns a recovery code.
func (s *Server) spendSecondFactor(user, code string) (bool, error) {
	sf, ok := s.secondFactor()
	if !ok {
		return false, nil
	}
	e, found, err := sf.TOTPEnrollment(user)
	if err != nil || !found || !e.Enabled {
		return false, err
	}
	code = strings.TrimSpace(code)
	if step, matched := totp.Verify(e.Secret, code, time.Now(), totpSkew); matched {
		// Verify only says the code belongs to this secret and window. The store
		// decides whether that step has already been spent, which is what stops a
		// code observed once from being replayed inside its own window.
		return sf.ConsumeTOTPStep(user, step)
	}
	return sf.ConsumeRecoveryCode(user, code)
}

// secondFactorJSON is the SPA's view of the account's own enrollment.
type secondFactorJSON struct {
	Enabled           bool `json:"enabled"`
	Pending           bool `json:"pending"`
	RecoveryRemaining int  `json:"recoveryRemaining"`
}

func (s *Server) handleSecondFactorStatus(w http.ResponseWriter, r *http.Request) {
	c, ok := s.session(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	sf, ok := s.secondFactor()
	if !ok {
		writeJSON(w, http.StatusOK, secondFactorJSON{})
		return
	}
	e, found, err := sf.TOTPEnrollment(c.Email)
	if err != nil {
		logError("second-factor-status", err, logging.Fields{"user": c.Email})
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	out := secondFactorJSON{Enabled: found && e.Enabled, Pending: found && !e.Enabled}
	if out.Enabled {
		if n, err := sf.RecoveryCodesRemaining(c.Email); err == nil {
			out.RecoveryRemaining = n
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// handleSecondFactorBegin mints a fresh secret and returns it with the
// otpauth:// URI the authenticator app scans. The secret is stored as a pending
// enrollment, so nothing is gated until a code confirms it.
func (s *Server) handleSecondFactorBegin(w http.ResponseWriter, r *http.Request) {
	c, ok := s.session(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	sf, ok := s.secondFactor()
	if !ok {
		writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "the second factor is unavailable"})
		return
	}
	secret, err := totp.NewSecret()
	if err != nil {
		logError("second-factor-begin", err, logging.Fields{"user": c.Email})
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	if err := sf.BeginTOTPEnrollment(c.Email, secret); err != nil {
		if errors.Is(err, directory.ErrTOTPAlreadyEnabled) {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "the second factor is already enabled"})
			return
		}
		logError("second-factor-begin", err, logging.Fields{"user": c.Email})
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"secret": secret,
		"uri":    totp.ProvisioningURI(secret, c.Email, s.hostname),
	})
}

// handleSecondFactorActivate turns the pending enrollment on once the user has
// produced a code from it, and returns the recovery codes. The codes are shown
// exactly here and never again, because only their hashes are stored.
func (s *Server) handleSecondFactorActivate(w http.ResponseWriter, r *http.Request) {
	c, ok := s.session(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	sf, ok := s.secondFactor()
	if !ok {
		writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "the second factor is unavailable"})
		return
	}
	var req struct {
		Code string `json:"code"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad request"})
		return
	}
	e, found, err := sf.TOTPEnrollment(c.Email)
	if err != nil {
		logError("second-factor-activate", err, logging.Fields{"user": c.Email})
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	if !found || e.Enabled {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "no enrollment is pending"})
		return
	}
	step, matched := totp.Verify(e.Secret, strings.TrimSpace(req.Code), time.Now(), totpSkew)
	if !matched {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid code"})
		return
	}
	codes, hashes, err := totp.NewRecoveryCodes()
	if err == nil {
		// The proving step is recorded with the activation, so the code the user
		// just typed cannot also serve as their first login.
		err = sf.ActivateTOTP(c.Email, step, hashes)
	}
	if err != nil {
		logError("second-factor-activate", err, logging.Fields{"user": c.Email})
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"recoveryCodes": codes})
}

// handleSecondFactorDisable removes the enrollment. It asks for the account
// password again, because turning the second factor off is exactly what someone
// who has stolen a live session would do first.
func (s *Server) handleSecondFactorDisable(w http.ResponseWriter, r *http.Request) {
	c, ok := s.session(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	sf, ok := s.secondFactor()
	if !ok {
		writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "the second factor is unavailable"})
		return
	}
	var req struct {
		Password string `json:"password"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad request"})
		return
	}
	addr := serve.ClientAddr(r)
	if !s.limiter.Allowed(addr, c.Email) {
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "too many failed attempts, try again later"})
		return
	}
	if _, ok := s.auth.Authenticate(c.Email, req.Password); !ok {
		s.limiter.Fail(addr, c.Email)
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid credentials"})
		return
	}
	s.limiter.Succeed(addr, c.Email)
	if err := sf.DisableTOTP(c.Email); err != nil {
		logError("second-factor-disable", err, logging.Fields{"user": c.Email})
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"disabled": true})
}
