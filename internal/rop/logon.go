package rop

import (
	"crypto/sha256"
	"encoding/binary"
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
	if _, err := p.Raw(int(essdnSize)); err != nil { // Essdn (session is keyed by the authenticated mailbox)
		return false
	}

	st, err := objectstore.Open(s.mailbox)
	if err != nil {
		writeErr(out, ropLogon, hindex, ecError)
		return true
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
	out.Uint8(0)                               // ResponseFlags
	out.GUID(deriveGUID("mailbox", s.mailbox)) // MailboxGuid
	out.Uint16(privateReplID)                  // ReplId
	out.GUID(deriveGUID("replica", s.mailbox)) // ReplGuid
	pushLogonTime(out, time.Now().UTC())       // LogonTime (8 bytes)
	out.Uint64(0)                              // GwartTime
	out.Uint32(0)                              // StoreState
	return true
}

// ropRelease handles RopRelease ([MS-OXCROPS] 2.2.15.3): it frees the referenced
// handle and emits no response (Release has no response buffer).
func (s *Session) ropRelease(handles []uint32, hindex uint8) {
	s.release(handleAt(handles, hindex))
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

// deriveGUID builds a stable GUID for a mailbox-scoped purpose from a hash of
// the purpose and mailbox path. The value is opaque to the client (it uses the
// pair for replica identity within the session) and stays constant for a given
// mailbox across the session.
func deriveGUID(purpose, mailbox string) mapi.GUID {
	sum := sha256.Sum256([]byte("hermex-rop:" + purpose + ":" + mailbox))
	var g mapi.GUID
	g.Data1 = binary.LittleEndian.Uint32(sum[0:4])
	g.Data2 = binary.LittleEndian.Uint16(sum[4:6])
	g.Data3 = binary.LittleEndian.Uint16(sum[6:8])
	copy(g.Data4[:], sum[8:16])
	return g
}
