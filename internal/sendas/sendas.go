// Package sendas decides which From identity an authenticated caller may put on a
// message, and which grant lets them.
//
// Every surface that accepts a client-chosen From needs this decision, and none of
// them traverses authenticated SMTP where the envelope gate runs: webmail2's
// /mail/send, EWS CreateItem, ActiveSync SendMail and the SMTP session's own MAIL
// FROM check each asked it separately. One implementation keeps them from drifting,
// which matters because the answer is a forgery gate: a surface that gets it wrong
// lets a caller write mail under somebody else's identity.
//
// It fails closed. An address that resolves to no local mailbox, a store that will
// not open, and a list that will not read all deny the grant rather than risk a
// forged From.
package sendas

import (
	"strings"

	"hermex/internal/directory"
	"hermex/internal/objectstore"
)

// Grant names how a caller came by the identity they asked to send under.
type Grant int

const (
	// GrantNone is the refusal: the caller holds no claim to the address.
	GrantNone Grant = iota
	// GrantOwn is the caller's own account, by its login or one of its aliases.
	GrantOwn
	// GrantSendAs is a send-as grant from another mailbox. The message names only
	// that mailbox: From carries it and no Sender header is written.
	GrantSendAs
	// GrantOnBehalf is a send-on-behalf-of grant from another mailbox. The message
	// names both: From carries the mailbox and Sender carries the real caller.
	GrantOnBehalf
)

// String renders a grant for a log field.
func (g Grant) String() string {
	switch g {
	case GrantOwn:
		return "own"
	case GrantSendAs:
		return "send-as"
	case GrantOnBehalf:
		return "on-behalf"
	}
	return "none"
}

// Resolve authorizes the From identity a caller asked for.
//
// representing is the address to put in From and sender the address to put in
// Sender; oxcmail writes a Sender header only when the two differ, so a send-as
// grant returns them equal and an on-behalf grant returns the caller in sender.
// An empty or self-matching want is the caller's own send. A refused identity
// returns GrantNone and two empty strings, never a fallback to the caller: storing
// a message under an identity the caller does not hold is a forgery, not a default.
func Resolve(accounts directory.Accounts, caller, want string) (representing, sender string, g Grant) {
	want = strings.ToLower(strings.TrimSpace(want))
	if want == "" || strings.EqualFold(want, caller) {
		return caller, caller, GrantOwn
	}
	ids := Identities(accounts, caller)
	// An alias of the caller's own account: the message names the alias alone.
	if containsFold(ids, want) {
		return want, want, GrantOwn
	}
	switch grantOf(accounts, want, ids) {
	case GrantSendAs:
		return want, want, GrantSendAs
	case GrantOnBehalf:
		return want, caller, GrantOnBehalf
	}
	return "", "", GrantNone
}

// Allows reports whether the caller may put want on a message at all. The SMTP
// session uses it for MAIL FROM and ActiveSync for a device-composed From, where
// only the permission matters and the Sender header is already whatever the client
// wrote.
//
// An empty want is refused here, unlike in Resolve. Resolve answers "which identity
// did the caller choose", and choosing none means their own. Allows answers "may
// this address be on the message", and an authenticated submission with a null
// sender names no identity to authorize.
func Allows(accounts directory.Accounts, caller, want string) bool {
	if strings.TrimSpace(want) == "" {
		return false
	}
	_, _, g := Resolve(accounts, caller, want)
	return g != GrantNone
}

// grantOf reads the grant the mailbox that owns want extends to the caller. A
// send-as grant wins over an on-behalf grant, because it is the narrower message:
// a caller who holds both should not have the real sender disclosed.
func grantOf(accounts directory.Accounts, want string, ids []string) Grant {
	path, ok := accounts.Resolve(want)
	if !ok {
		return GrantNone
	}
	st, err := objectstore.Open(path)
	if err != nil {
		return GrantNone
	}
	defer st.Close()
	if list, err := st.GetSendAs(); err == nil && grants(list, ids) {
		return GrantSendAs
	}
	if list, err := st.GetSendOnBehalf(); err == nil && grants(list, ids) {
		return GrantOnBehalf
	}
	return GrantNone
}

// Identities returns every address the caller owns, falling back to the login alone
// when the directory cannot enumerate them. Falling back to the login keeps a caller
// able to send as themselves and as no one else.
func Identities(accounts directory.Accounts, caller string) []string {
	id, ok := accounts.(directory.Identifier)
	if !ok {
		return []string{caller}
	}
	addrs, err := id.Identities(caller)
	if err != nil || len(addrs) == 0 {
		return []string{caller}
	}
	return addrs
}

// grants reports whether a grant list names any of the caller's identities.
func grants(list, ids []string) bool {
	for _, g := range list {
		if containsFold(ids, strings.ToLower(strings.TrimSpace(g))) {
			return true
		}
	}
	return false
}

// containsFold reports whether want (already lowercased) equals any address in list,
// compared case-insensitively after trimming.
func containsFold(list []string, want string) bool {
	for _, a := range list {
		if strings.ToLower(strings.TrimSpace(a)) == want {
			return true
		}
	}
	return false
}
