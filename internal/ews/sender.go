package ews

import (
	"hermex/internal/mapi"
	"hermex/internal/sendas"
)

// resolveSender authorizes the From identity a client chose and returns the address to
// represent plus the address to name in Sender. CreateItem never traverses the
// authenticated SMTP path, so this is the gate that keeps a client from writing mail as
// somebody else. The decision itself lives in internal/sendas, shared with the other
// three surfaces that accept a client-chosen From, because a gate that drifts between
// them is a forgery waiting on whichever one drifted.
//
// oxcmail writes a Sender header only when the two differ, so a send-as grant returns
// them equal and an on-behalf grant returns the caller in sender.
func (s *Server) resolveSender(caller, want string) (representing, sender string, ok bool) {
	representing, sender, g := sendas.Resolve(s.accounts, caller, want)
	return representing, sender, g != sendas.GrantNone
}

// stampDraftSender authorizes the From a saved draft carries and rewrites the
// representing and sender identities the grant yields, so an on-behalf send names
// the caller in Sender whoever wrote the draft. It reports false when the caller
// holds no claim to that From.
func (s *Server) stampDraftSender(props *mapi.PropertyValues, caller string) bool {
	v, _ := props.Get(mapi.PrSentRepresentingSmtpAddress)
	want, _ := v.(string)
	representing, sender, ok := s.resolveSender(caller, want)
	if !ok {
		return false
	}
	props.Set(mapi.PrSentRepresentingSmtpAddress, representing)
	props.Set(mapi.PrSentRepresentingEmailAddress, representing)
	props.Set(mapi.PrSentRepresentingAddrType, "SMTP")
	props.Set(mapi.PrSenderSmtpAddress, sender)
	props.Set(mapi.PrSenderEmailAddress, sender)
	props.Set(mapi.PrSenderAddrType, "SMTP")
	return true
}
