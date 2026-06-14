package rop

import (
	"bytes"
	"testing"

	"hermex/internal/ext"
	"hermex/internal/mapi"
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
// the exact LOGON_PMB_RESPONSE field order — header, the 13 special-folder EIDs
// (replica id 1), ResponseFlags, MailboxGuid, ReplId=5, ReplGuid, an 8-byte
// LogonTime, GwartTime, StoreState — and registers a logon object at the slot.
func TestRopLogonResponse(t *testing.T) {
	dir := t.TempDir()
	sess := NewSession(dir)
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
	if got := mustU8(t, p, "RopId"); got != ropLogon {
		t.Errorf("RopId = %#x, want %#x", got, ropLogon)
	}
	if got := mustU8(t, p, "OutputHandleIndex"); got != 0 {
		t.Errorf("OutputHandleIndex = %d, want 0", got)
	}
	if got := mustU32(t, p, "ReturnValue"); got != ecSuccess {
		t.Errorf("ReturnValue = %#x, want 0", got)
	}
	if got := mustU8(t, p, "LogonFlags"); got != logonFlags {
		t.Errorf("LogonFlags = %#x, want %#x", got, logonFlags)
	}
	for i, fid := range logonFolderFIDs {
		want := uint64(mapi.MakeEIDEx(1, fid))
		if got := mustU64(t, p, "FolderId"); got != want {
			t.Errorf("FolderId[%d] = %#x, want %#x", i, got, want)
		}
	}
	if got := mustU8(t, p, "ResponseFlags"); got != 0 {
		t.Errorf("ResponseFlags = %#x, want 0", got)
	}
	mbg := mustGUID(t, p, "MailboxGuid")
	if want := deriveGUID("mailbox", dir); mbg != want {
		t.Errorf("MailboxGuid = %s, want %s", mbg, want)
	}
	if got := mustU16(t, p, "ReplId"); got != privateReplID {
		t.Errorf("ReplId = %d, want %d", got, privateReplID)
	}
	rg := mustGUID(t, p, "ReplGuid")
	if want := deriveGUID("replica", dir); rg != want {
		t.Errorf("ReplGuid = %s, want %s", rg, want)
	}
	if mbg == rg {
		t.Error("MailboxGuid and ReplGuid must be distinct")
	}
	// LogonTime: 6 bytes + a 16-bit year. The year must be a real decomposed
	// time, not a zeroed field.
	for _, f := range []string{"Sec", "Min", "Hour", "DoW", "Day", "Month"} {
		mustU8(t, p, "LogonTime."+f)
	}
	if year := mustU16(t, p, "LogonTime.Year"); year < 2020 {
		t.Errorf("LogonTime year = %d, want a real current year", year)
	}
	if got := mustU64(t, p, "GwartTime"); got != 0 {
		t.Errorf("GwartTime = %#x, want 0", got)
	}
	if got := mustU32(t, p, "StoreState"); got != 0 {
		t.Errorf("StoreState = %#x, want 0", got)
	}
	if p.Remaining() != 0 {
		t.Errorf("trailing bytes after LOGON_PMB_RESPONSE: %d", p.Remaining())
	}
}

// TestRopRelease confirms Release frees the handle and emits no response bytes.
func TestRopRelease(t *testing.T) {
	sess := NewSession(t.TempDir())
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
	sess := NewSession(t.TempDir())
	defer sess.Close()

	const unknown = 0x55
	resp, _ := sess.Dispatch([]byte{unknown, 0x00, 0x02}, []uint32{0, 0, 0xFFFFFFFF})
	want := []byte{unknown, 0x02, 0x05, 0x40, 0x00, 0x80} // ec 0x80004005, little-endian
	if !bytes.Equal(resp, want) {
		t.Errorf("unknown-ROP response = % x, want % x", resp, want)
	}
}

// TestDispatchEmpty confirms an empty ROP list yields an empty response.
func TestDispatchEmpty(t *testing.T) {
	sess := NewSession(t.TempDir())
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
