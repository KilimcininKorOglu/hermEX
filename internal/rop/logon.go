package rop

import (
	"strings"
	"time"

	"hermex/internal/ext"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// logonFolderFIDs are the 13 private-mailbox special folders RopLogon returns,
// in the fixed LOGON_PMB_RESPONSE order ([MS-OXCSTOR] 2.2.1.1.3): Root,
// DeferredAction, SpoolerQueue, IPM-subtree, Inbox, Outbox, Sent, Deleted,
// CommonViews, Schedule, Finder, Views, Shortcuts.
var logonFolderFIDs = [13]uint64{
	mapi.PrivateFIDRoot, mapi.PrivateFIDDeferredAction, mapi.PrivateFIDSpoolerQueue,
	mapi.PrivateFIDIPMSubtree, mapi.PrivateFIDInbox, mapi.PrivateFIDOutbox,
	mapi.PrivateFIDSentItems, mapi.PrivateFIDDeletedItems, mapi.PrivateFIDCommonViews,
	mapi.PrivateFIDSchedule, mapi.PrivateFIDFinder, mapi.PrivateFIDViews,
	mapi.PrivateFIDShortcuts,
}

// privateReplID is the replica id a private-mailbox logon reports in
// LOGON_PMB_RESPONSE. The special-folder entry ids themselves carry replica
// id 1 (the store's own replica), per the EID encoding below.
const privateReplID uint16 = 5

// LOGON_PMB_RESPONSE ResponseFlags bits ([MS-OXCSTOR] 2.2.1.1.3).
const (
	responseFlagReserved    = 0x01
	responseFlagOwnerRight  = 0x02
	responseFlagSendAsRight = 0x04
)

// ownerResponseFlags is what a single-owner mailbox logon reports: the
// authenticated user owns the mailbox, so it carries owner and send-as rights.
// (OOF state is not folded in here; v1 keeps it in the webmail settings.)
const ownerResponseFlags = responseFlagReserved | responseFlagOwnerRight | responseFlagSendAsRight

// ropLogon handles RopLogon ([MS-OXCSTOR] 2.2.1.1): it opens the mailbox store,
// registers the logon object at the output handle slot, and writes the private
// LOGON_PMB_RESPONSE. It returns false only when the request body is malformed
// (dispatch then stops); a store-open failure is reported in band as an error
// ReturnValue (a well-formed response), so dispatch can continue.
func (s *Session) ropLogon(p *ext.Pull, out *ext.Push, handles []uint32, hindex uint8) bool {
	logonFlags, e1 := p.Uint8()
	_, e2 := p.Uint32() // OpenFlags
	_, e3 := p.Uint32() // StoreState
	essdnSize, e4 := p.Uint16()
	if e1 != nil || e2 != nil || e3 != nil || e4 != nil {
		return false
	}
	essdn, err := p.Raw(int(essdnSize))
	if err != nil {
		return false
	}

	// Resolve which mailbox this logon opens. The Essdn names the target; only a
	// positively-resolved DIFFERENT mailbox is a delegate open. An empty,
	// unparseable, or self-resolving Essdn opens the caller's own mailbox, so an
	// owner is never misrouted or locked out (even when logged in under an alias
	// whose Essdn names the primary address).
	maildir := s.mailbox
	delegate := false
	if essdnSize > 0 && s.accounts != nil {
		if smtp, ok := essdnToSMTP(string(essdn)); ok {
			if md, ok := s.accounts.Resolve(smtp); ok && md != s.mailbox {
				maildir = md
				delegate = true
			}
		}
	}

	st, err := objectstore.Open(maildir)
	if err != nil {
		writeErr(out, ropLogon, hindex, ecError)
		return true
	}
	if delegate {
		// A delegate may open the mailbox only with some access to it — a designated
		// delegate, or a grant on any folder. The per-folder gates then govern what
		// they can actually read and change.
		ok, err := s.mayOpenDelegate(st, s.owner)
		if err != nil {
			_ = st.Close()
			writeErr(out, ropLogon, hindex, ecError)
			return true
		}
		if !ok {
			_ = st.Close()
			writeErr(out, ropLogon, hindex, ecAccessDenied)
			return true
		}
	}
	// Mirror the reference logon's identity: MailboxGuid is the store record key
	// (the mailbox GUID) and ReplGuid is the mapping signature, both persisted at
	// mailbox creation, so the entry ids the store later hands out resolve
	// against the same GUIDs this logon advertises.
	mailboxGUID, errM := st.StoreGUID()
	replGUID, errR := st.MappingSignature()
	if errM != nil || errR != nil {
		_ = st.Close()
		writeErr(out, ropLogon, hindex, ecError)
		return true
	}
	if delegate {
		// Register the caller as this store's delegate so every folder/message op
		// authorizes against the caller's permissions (the owner short-circuit in
		// authorize covers an owner logon, which is never registered here).
		s.delegateCallers[st] = s.owner
	}
	h := s.alloc(&object{kind: kindLogon, store: st})
	setHandle(handles, hindex, h)

	// Response header: RopId, OutputHandleIndex (echoed), ReturnValue.
	out.Uint8(ropLogon)
	out.Uint8(hindex)
	out.Uint32(ecSuccess)
	// LOGON_PMB_RESPONSE body.
	out.Uint8(logonFlags)
	for _, fid := range logonFolderFIDs {
		out.Uint64(uint64(mapi.MakeEIDEx(1, fid)))
	}
	// An owner logon carries owner + send-as rights; a delegate logon carries
	// neither in v1 (send-on-behalf, which would add the send-as right, is a later
	// increment) — only the reserved bit.
	responseFlags := uint8(ownerResponseFlags)
	if delegate {
		responseFlags = responseFlagReserved
	}
	out.Uint8(responseFlags)             // ResponseFlags
	out.GUID(mailboxGUID)                // MailboxGuid (PR_STORE_RECORD_KEY)
	out.Uint16(privateReplID)            // ReplId
	out.GUID(replGUID)                   // ReplGuid (PR_MAPPING_SIGNATURE)
	pushLogonTime(out, time.Now().UTC()) // LogonTime (8 bytes)
	out.Uint64(0)                        // GwartTime
	out.Uint32(0)                        // StoreState
	return true
}

// ropRelease handles RopRelease ([MS-OXCROPS] 2.2.15.3): it frees the referenced
// handle and emits no response (Release has no response buffer).
func (s *Session) ropRelease(handles []uint32, hindex uint8) {
	s.release(handleAt(handles, hindex))
}

// essdnToSMTP recovers the target mailbox's SMTP address from a RopLogon Essdn —
// an X500 address-book DN whose final "/cn=" component is the SMTP address, the
// reversible form the GAL hands out. ok is false when the DN carries no such
// component (so the caller falls back to opening its own mailbox). The match is
// case-insensitive on the "/cn=" stem; the address case is preserved for the
// directory lookup. A trailing NUL (the wire string terminator) is trimmed.
func essdnToSMTP(dn string) (string, bool) {
	dn = strings.TrimRight(dn, "\x00")
	i := strings.LastIndex(strings.ToLower(dn), "/cn=")
	if i < 0 {
		return "", false
	}
	smtp := dn[i+len("/cn="):]
	if smtp == "" || !strings.Contains(smtp, "@") {
		return "", false
	}
	return smtp, true
}

// mayOpenDelegate reports whether caller may open store as a delegate: they are a
// designated delegate of the mailbox, or they hold an explicit per-user grant on any
// of its folders. The open gate is deliberately caller-specific — it counts only the
// caller's OWN grants, never the universal "default" member, so the always-present
// default free/busy grant does not let every authenticated user open every mailbox.
// (The per-folder gates then govern what the opened session can actually read and
// change, and those DO honour the default grant — open vs per-folder use different
// criteria on purpose.) A default-member or group grant alone does not enable a
// store-open; that is the documented v1 limitation (free/busy is served via
// NSPI/EWS/CalDAV, not a ROP logon, so free/busy sharing is unaffected).
func (s *Session) mayOpenDelegate(store *objectstore.Store, caller string) (bool, error) {
	delegates, err := store.GetDelegates()
	if err != nil {
		return false, err
	}
	for _, d := range delegates {
		if strings.EqualFold(d, caller) {
			return true, nil
		}
	}
	return store.HasFolderGrant(caller)
}

// pushLogonTime serializes a LogonTime ([MS-OXCROPS] 2.2.1.1.3): Seconds,
// Minutes, Hour, DayOfWeek (Sunday=0), Day, Month each as a byte, then Year as
// a 16-bit value.
func pushLogonTime(out *ext.Push, t time.Time) {
	out.Uint8(uint8(t.Second()))
	out.Uint8(uint8(t.Minute()))
	out.Uint8(uint8(t.Hour()))
	out.Uint8(uint8(t.Weekday()))
	out.Uint8(uint8(t.Day()))
	out.Uint8(uint8(t.Month()))
	out.Uint16(uint16(t.Year()))
}
