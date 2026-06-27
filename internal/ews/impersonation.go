package ews

import (
	"net/http"

	"hermex/internal/logging"
	"hermex/internal/objectstore"
)

// ExchangeImpersonation ([MS-OXWSCDATA] 2.2.4.14) lets an authenticated caller act
// on another user's mailbox by carrying a <t:ExchangeImpersonation><t:ConnectingSID>
// SOAP header. hermEX authorizes it with the delegate model the rest of the server
// uses for cross-mailbox access: the caller may impersonate a target only when the
// target has named the caller a delegate (objectstore GetDelegates), or when the
// target is the caller's own mailbox. The header is processed before the operation,
// so a failure is a SOAP Fault rather than a per-operation response.
//
// On success the session's effective identity (user + mailbox) becomes the target
// so every handler operates on the target's store, while realUser keeps the
// authenticated principal for the audit log. SID-based ConnectingSID is not
// supported (hermEX resolves identities by address, not by Windows SID).

// impersonationTarget is the resolved intent of an ExchangeImpersonation header:
// the address to impersonate, or a flag that only an unsupported SID was supplied.
type impersonationTarget struct {
	addr  string
	isSID bool
}

// applyImpersonation gates and applies an ExchangeImpersonation header. It returns
// true to proceed (with sess swapped to the target on success) and false when it
// has already written a SOAP Fault. A nil target means no header was present.
func (s *Server) applyImpersonation(w http.ResponseWriter, sess *session, imp *impersonationTarget) bool {
	if imp == nil {
		return true
	}
	if imp.isSID {
		writeSOAPFault(w, "ErrorImpersonationFailed", "ExchangeImpersonation: SID-based ConnectingSID is not supported")
		return false
	}
	if imp.addr == "" {
		return true // an empty ConnectingSID: act as the authenticated user
	}
	// Impersonating one's own mailbox is a permitted no-op.
	if s.isOwnMailbox(sess, imp.addr) {
		return true
	}
	// The denial path is deliberately identical for an unknown target and a known
	// target that has not delegated to the caller, so the response is not a
	// mailbox-existence oracle (OWASP A01).
	targetPath, ok := s.accounts.Resolve(imp.addr)
	if !ok {
		writeSOAPFault(w, "ErrorImpersonateUserDenied", "ExchangeImpersonation: not permitted")
		return false
	}
	st, err := objectstore.Open(targetPath)
	if err != nil {
		writeSOAPFault(w, "ErrorInternalServerError", err.Error())
		return false
	}
	delegates, err := st.GetDelegates()
	st.Close()
	if err != nil {
		writeSOAPFault(w, "ErrorInternalServerError", err.Error())
		return false
	}
	if !containsFold(delegates, sess.realUser) {
		writeSOAPFault(w, "ErrorImpersonateUserDenied", "ExchangeImpersonation: not permitted")
		return false
	}
	sess.user = imp.addr
	sess.mailbox = targetPath
	sess.impersonating = imp.addr
	return true
}

// operationFields builds the audit fields for an operation event: the op name plus,
// when the request is impersonated, the target so the log shows the real principal
// (Event.User) acting as the target.
func operationFields(op string, sess *session) logging.Fields {
	f := logging.Fields{"op": op}
	if sess.impersonating != "" {
		f["impersonating"] = sess.impersonating
	}
	return f
}
