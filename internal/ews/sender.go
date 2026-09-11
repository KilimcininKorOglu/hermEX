package ews

import "hermex/internal/sendas"

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
