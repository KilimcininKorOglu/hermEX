package rop

import (
	"encoding/binary"
	"testing"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// ropReturn reads the ReturnValue of a single ROP response.
func ropReturn(t *testing.T, resp []byte) uint32 {
	t.Helper()
	if len(resp) < 6 {
		t.Fatalf("short response %x", resp)
	}
	return binary.LittleEndian.Uint32(resp[2:6])
}

// TestPropertyReadOfARemovedObject proves a property read on a handle whose
// message was removed underneath it answers ecNotFound, the code the store
// failure carries, rather than a generic ecError or an empty success.
func TestPropertyReadOfARemovedObject(t *testing.T) {
	dir := provisionedDir(t)
	msgID := seedInboxMessage(t, dir, "GONE")
	sess := NewSession(dir, nil, "")
	defer sess.Close()
	h := dispatchOK(t, sess, logonRequest(0, 0x01), ropLogon, "Logon", 0xFFFFFFFF)
	h = dispatchOK(t, sess, buildOpenMessage(0, 1,
		uint64(mapi.MakeEIDEx(1, mapi.PrivateFIDInbox)), uint64(mapi.MakeEIDEx(1, uint64(msgID)))), ropOpenMessage, "OpenMessage", h[0], 0xFFFFFFFF)
	msgH := h[1]

	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteObject(msgID); err != nil {
		mustClose(t, st)
		t.Fatal(err)
	}
	mustClose(t, st)

	expectReadsNotFound(t, sess, msgH, "message")
}

// TestPropertyReadOfARemovedFolder proves the same for a folder handle whose
// folder was deleted underneath it.
func TestPropertyReadOfARemovedFolder(t *testing.T) {
	dir := provisionedDir(t)
	seedInboxMessage(t, dir, "SEED")
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	parent := int64(mapi.PrivateFIDInbox)
	fid, err := st.CreateFolder(&parent, "Doomed")
	mustClose(t, st)
	if err != nil {
		t.Fatal(err)
	}
	sess := NewSession(dir, nil, "")
	defer sess.Close()
	h := dispatchOK(t, sess, logonRequest(0, 0x01), ropLogon, "Logon", 0xFFFFFFFF)
	h = dispatchOK(t, sess, buildOpenFolder(0, 1, uint64(mapi.MakeEIDEx(1, uint64(fid)))), ropOpenFolder, "OpenFolder", h[0], 0xFFFFFFFF)
	folderH := h[1]

	st, err = objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteFolder(fid); err != nil {
		mustClose(t, st)
		t.Fatal(err)
	}
	mustClose(t, st)
	expectReadsNotFound(t, sess, folderH, "folder")
}

// expectReadsNotFound runs GetPropertiesAll, GetPropertiesSpecific and
// GetPropertiesList on handle and expects ecNotFound from each.
func expectReadsNotFound(t *testing.T, sess *Session, handle uint32, what string) {
	t.Helper()
	for _, op := range []uint8{ropGetPropertiesAll, ropGetPropertiesSpecific, ropGetPropertiesList} {
		var req []byte
		if op == ropGetPropertiesList {
			req = []byte{op, 0, 0}
		} else {
			req = buildGetProps(op, 0, []mapi.PropTag{mapi.PrSubject})
		}
		resp := dispatchOn(sess, req, handle)
		if len(resp) < 6 || resp[0] != op {
			t.Errorf("rop 0x%02x on a removed %s returned bad head %x", op, what, resp)
			continue
		}
		if got := ropReturn(t, resp); got != ecNotFound {
			t.Errorf("rop 0x%02x on a removed %s returned 0x%08x, want ecNotFound", op, what, got)
		}
	}
}
