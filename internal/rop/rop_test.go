package rop

import (
	"bytes"
	"testing"

	"hermex/internal/ext"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// logonRequest builds a well-formed RopLogon request ROP (header + private
// LOGON_REQUEST body) targeting the given output handle slot.
func logonRequest(hindex uint8, logonFlags uint8) []byte {
	rb := ext.NewPush(ext.FlagUTF16)
	rb.Uint8(ropLogon) // RopId
	rb.Uint8(0)        // LogonId
	rb.Uint8(hindex)   // OutputHandleIndex
	rb.Uint8(logonFlags)
	rb.Uint32(0) // OpenFlags
	rb.Uint32(0) // StoreState
	rb.Uint16(0) // EssdnSize (no Essdn; the session is keyed by the mailbox)
	return rb.Bytes()
}

// TestRopLogonResponse is the byte-layout keystone: it asserts RopLogon emits
// the exact LOGON_PMB_RESPONSE field order, header, the 13 special-folder EIDs
// (replica id 1), ResponseFlags, MailboxGuid, ReplId=5, ReplGuid, an 8-byte
// LogonTime, GwartTime, StoreState, and registers a logon object at the slot.
func TestRopLogonResponse(t *testing.T) {
	dir := t.TempDir()
	sess := NewSession(dir, nil, "")
	defer sess.Close()

	const logonFlags = 0x01 // Private
	resp, handles := sess.Dispatch(logonRequest(0, logonFlags), []uint32{0xFFFFFFFF})

	// The output slot now carries a real (non-null) handle bound to a logon object.
	if len(handles) != 1 || handles[0] == 0xFFFFFFFF {
		t.Fatalf("output handle not set: %v", handles)
	}
	obj := sess.handles[handles[0]]
	if obj == nil || obj.kind != kindLogon || obj.store == nil {
		t.Fatalf("handle %d is not a bound logon object: %+v", handles[0], obj)
	}

	p := ext.NewPull(resp, ext.FlagUTF16)
	wantU8(t, p, "RopId", ropLogon)
	wantU8(t, p, "OutputHandleIndex", 0)
	wantU32(t, p, "ReturnValue", ecSuccess)
	wantU8(t, p, "LogonFlags", logonFlags)
	for _, fid := range logonFolderFIDs {
		wantU64(t, p, "FolderId", uint64(mapi.MakeEIDEx(1, fid)))
	}
	wantU8(t, p, "ResponseFlags", ownerResponseFlags)
	assertLogonIdentity(t, p, obj.store)
	assertLogonTime(t, p)
	wantU64(t, p, "GwartTime", 0)
	wantU32(t, p, "StoreState", 0)
	wantDrained(t, p, "LOGON_PMB_RESPONSE")
}

// assertLogonIdentity checks the two GUIDs and the replica id the response
// carries. MailboxGuid is the store record key and ReplGuid is the mapping
// signature, both sourced from the store's persisted identity rather than
// derived ad hoc, so they must match the store and must differ from each other.
func assertLogonIdentity(t *testing.T, p *ext.Pull, store *objectstore.Store) {
	t.Helper()
	wantMbg, err := store.StoreGUID()
	if err != nil {
		t.Fatalf("StoreGUID: %v", err)
	}
	wantRg, err := store.MappingSignature()
	if err != nil {
		t.Fatalf("MappingSignature: %v", err)
	}
	wantGUID(t, p, "MailboxGuid", wantMbg)
	wantU16(t, p, "ReplId", privateReplID)
	wantGUID(t, p, "ReplGuid", wantRg)
	if wantMbg == wantRg {
		t.Error("store record key and mapping signature must be distinct GUIDs")
	}
}

// assertLogonTime checks the decomposed LogonTime: six bytes and a 16-bit year.
// The year proves the field carries a real time rather than zeros.
func assertLogonTime(t *testing.T, p *ext.Pull) {
	t.Helper()
	for _, f := range []string{"Sec", "Min", "Hour", "DoW", "Day", "Month"} {
		mustU8(t, p, "LogonTime."+f)
	}
	if year := mustU16(t, p, "LogonTime.Year"); year < 2020 {
		t.Errorf("LogonTime year = %d, want a real current year", year)
	}
}

// TestRopRelease confirms Release frees the handle and emits no response bytes.
func TestRopRelease(t *testing.T) {
	sess := NewSession(t.TempDir(), nil, "")
	defer sess.Close()

	_, handles := sess.Dispatch(logonRequest(0, 0x01), []uint32{0xFFFFFFFF})
	h := handles[0]
	if sess.handles[h] == nil {
		t.Fatalf("logon handle %d not registered", h)
	}

	rel := []byte{ropRelease, 0x00, 0x00} // RopId, LogonId, InputHandleIndex
	resp, _ := sess.Dispatch(rel, []uint32{h})
	if len(resp) != 0 {
		t.Errorf("Release emitted %d response bytes, want 0", len(resp))
	}
	if sess.handles[h] != nil {
		t.Errorf("handle %d not freed after Release", h)
	}
}

// TestDispatchUnknownRop confirms an unimplemented ROP yields the 6-byte generic
// error (RopId, HandleIndex, ec) and that dispatch then stops.
func TestDispatchUnknownRop(t *testing.T) {
	sess := NewSession(t.TempDir(), nil, "")
	defer sess.Close()

	const unknown = 0xF0 // an unassigned ROP id (the dispatch implements no handler for it)
	resp, _ := sess.Dispatch([]byte{unknown, 0x00, 0x02}, []uint32{0, 0, 0xFFFFFFFF})
	want := []byte{unknown, 0x02, 0x05, 0x40, 0x00, 0x80} // ec 0x80004005, little-endian
	if !bytes.Equal(resp, want) {
		t.Errorf("unknown-ROP response = % x, want % x", resp, want)
	}
}

// TestDispatchEmpty confirms an empty ROP list yields an empty response.
func TestDispatchEmpty(t *testing.T) {
	sess := NewSession(t.TempDir(), nil, "")
	defer sess.Close()

	resp, handles := sess.Dispatch(nil, nil)
	if len(resp) != 0 || len(handles) != 0 {
		t.Errorf("empty dispatch = (%d bytes, %d handles), want (0, 0)", len(resp), len(handles))
	}
}

// --- pull helpers (fail the test on a short read) ---

func mustU8(t *testing.T, p *ext.Pull, field string) uint8 {
	t.Helper()
	v, err := p.Uint8()
	if err != nil {
		t.Fatalf("read %s: %v", field, err)
	}
	return v
}

func mustU16(t *testing.T, p *ext.Pull, field string) uint16 {
	t.Helper()
	v, err := p.Uint16()
	if err != nil {
		t.Fatalf("read %s: %v", field, err)
	}
	return v
}

func mustU32(t *testing.T, p *ext.Pull, field string) uint32 {
	t.Helper()
	v, err := p.Uint32()
	if err != nil {
		t.Fatalf("read %s: %v", field, err)
	}
	return v
}

func mustU64(t *testing.T, p *ext.Pull, field string) uint64 {
	t.Helper()
	v, err := p.Uint64()
	if err != nil {
		t.Fatalf("read %s: %v", field, err)
	}
	return v
}

func mustGUID(t *testing.T, p *ext.Pull, field string) mapi.GUID {
	t.Helper()
	v, err := p.GUID()
	if err != nil {
		t.Fatalf("read %s: %v", field, err)
	}
	return v
}

// --- assertion helpers (read one field and compare it) ---
//
// A wire test reads a field and compares it, over and over. Writing that as a
// read-then-if pair everywhere buries the one thing each line asserts, and every
// pair charges the enclosing test another branch. These read and compare in one
// call, so a test body reads as the field list it is checking.

func wantU8(t *testing.T, p *ext.Pull, field string, want uint8) {
	t.Helper()
	if got := mustU8(t, p, field); got != want {
		t.Errorf("%s = %#x, want %#x", field, got, want)
	}
}

func wantU16(t *testing.T, p *ext.Pull, field string, want uint16) {
	t.Helper()
	if got := mustU16(t, p, field); got != want {
		t.Errorf("%s = %d, want %d", field, got, want)
	}
}

func wantU32(t *testing.T, p *ext.Pull, field string, want uint32) {
	t.Helper()
	if got := mustU32(t, p, field); got != want {
		t.Errorf("%s = %#x, want %#x", field, got, want)
	}
}

func wantU64(t *testing.T, p *ext.Pull, field string, want uint64) {
	t.Helper()
	if got := mustU64(t, p, field); got != want {
		t.Errorf("%s = %#x, want %#x", field, got, want)
	}
}

func wantGUID(t *testing.T, p *ext.Pull, field string, want mapi.GUID) {
	t.Helper()
	if got := mustGUID(t, p, field); got != want {
		t.Errorf("%s = %s, want %s", field, got, want)
	}
}

// wantDrained asserts the reader consumed the whole response, which is what
// proves a layout test checked every field the server emitted.
func wantDrained(t *testing.T, p *ext.Pull, what string) {
	t.Helper()
	if n := p.Remaining(); n != 0 {
		t.Errorf("trailing bytes after %s: %d", what, n)
	}
}
