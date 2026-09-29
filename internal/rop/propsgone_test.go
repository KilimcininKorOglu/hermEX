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
	dir := t.TempDir()
	msgID := seedInboxMessage(t, dir, "GONE")
	sess := NewSession(dir, nil, "")
	defer sess.Close()
	_, h := sess.Dispatch(logonRequest(0, 0x01), []uint32{0xFFFFFFFF})
	_, h = sess.Dispatch(buildOpenMessage(0, 1,
		uint64(mapi.MakeEIDEx(1, mapi.PrivateFIDInbox)), uint64(mapi.MakeEIDEx(1, uint64(msgID)))), []uint32{h[0], 0xFFFFFFFF})
	msgH := h[1]

	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteObject(msgID); err != nil {
		st.Close()
		t.Fatal(err)
	}
	st.Close()

	expectReadsNotFound(t, sess, msgH, "message")
}

// TestPropertyReadOfARemovedFolder proves the same for a folder handle whose
// folder was deleted underneath it.
func TestPropertyReadOfARemovedFolder(t *testing.T) {
	dir := t.TempDir()
	seedInboxMessage(t, dir, "SEED")
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	parent := int64(mapi.PrivateFIDInbox)
	fid, err := st.CreateFolder(&parent, "Doomed")
	st.Close()
	if err != nil {
		t.Fatal(err)
	}
	sess := NewSession(dir, nil, "")
	defer sess.Close()
	_, h := sess.Dispatch(logonRequest(0, 0x01), []uint32{0xFFFFFFFF})
	resp, h := sess.Dispatch(buildOpenFolder(0, 1, uint64(mapi.MakeEIDEx(1, uint64(fid)))), []uint32{h[0], 0xFFFFFFFF})
	if got := ropReturn(t, resp); got != ecSuccess {
		t.Fatalf("OpenFolder returned 0x%08x", got)
	}
	folderH := h[1]

	st, err = objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteFolder(fid); err != nil {
		st.Close()
		t.Fatal(err)
	}
	st.Close()
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
		resp, _ := sess.Dispatch(req, []uint32{handle})
		if got := ropReturn(t, resp); got != ecNotFound {
			t.Errorf("rop 0x%02x on a removed %s returned 0x%08x, want ecNotFound", op, what, got)
		}
	}
}
