package ews

import (
	"strings"

	"hermex/internal/directory"
	"hermex/internal/objectstore"
)

// resolveSender authorizes the From identity a client chose and returns the address to
// represent plus the real authenticated caller. CreateItem never traverses the
// authenticated SMTP path, so this is the gate that keeps a client from writing mail as
// somebody else: an empty or self-matching want sends as the caller; any other address is
// allowed only when it is one of the caller's own directory identities (an alias), or when
// the mailbox that owns it granted the caller send-as. It fails closed, because storing a
// message under an identity the caller does not hold is a forgery, not a fallback.
//
// sender is always the real caller, so oxcmail emits a Sender header ("on behalf of")
// whenever the two differ.
func (s *Server) resolveSender(caller, want string) (representing, sender string, ok bool) {
	want = strings.ToLower(strings.TrimSpace(want))
	if want == "" || strings.EqualFold(want, caller) {
		return caller, caller, true
	}
	if s.callerOwns(caller, want) {
		return want, want, true
	}
	if s.grantedSendAs(caller, want) {
		return want, caller, true
	}
	return "", "", false
}

// callerOwns reports whether want is one of the caller's own directory identities, which
// is the case for an alias of the account.
func (s *Server) callerOwns(caller, want string) bool {
	id, isID := s.accounts.(directory.Identifier)
	if !isID {
		return false
	}
	addrs, err := id.Identities(caller)
	if err != nil {
		return false
	}
	for _, a := range addrs {
		if strings.EqualFold(strings.TrimSpace(a), want) {
			return true
		}
	}
	return false
}

// grantedSendAs reports whether the mailbox that owns want lists the caller in its send-as
// grants. It fails closed identically to the MTA gate: any resolution, open, or read
// failure denies the grant.
func (s *Server) grantedSendAs(caller, want string) bool {
	path, ok := s.accounts.Resolve(want)
	if !ok {
		return false
	}
	st, err := objectstore.Open(path)
	if err != nil {
		return false
	}
	defer st.Close()
	list, err := st.GetSendAs()
	if err != nil {
		return false
	}
	return grantsCaller(list, s.callerIdentities(caller))
}

// callerIdentities returns every address the caller owns, falling back to the login when
// the directory cannot report them.
func (s *Server) callerIdentities(caller string) []string {
	id, isID := s.accounts.(directory.Identifier)
	if !isID {
		return []string{caller}
	}
	addrs, err := id.Identities(caller)
	if err != nil || len(addrs) == 0 {
		return []string{caller}
	}
	return addrs
}

// grantsCaller reports whether a send-as list names any of the caller's identities.
func grantsCaller(list, ids []string) bool {
	for _, g := range list {
		for _, id := range ids {
			if strings.EqualFold(strings.TrimSpace(g), strings.TrimSpace(id)) {
				return true
			}
		}
	}
	return false
}
