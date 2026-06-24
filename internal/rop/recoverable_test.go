package rop

import (
	"testing"
	"time"

	"hermex/internal/ext"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// buildGetContentsTableFlags builds a RopGetContentsTable request with an explicit
// TableFlags byte (the shared helper hardcodes 0), so a test can set SHOW_SOFT_DELETES.
func buildGetContentsTableFlags(inIdx, outIdx, flags uint8) []byte {
	b := ext.NewPush(ext.FlagUTF16)
	b.Uint8(ropGetContentsTable)
	b.Uint8(0)
	b.Uint8(inIdx)
	b.Uint8(outIdx)
	b.Uint8(flags) // TableFlags
	return b.Bytes()
}

// TestContentsTableShowSoftDeletes proves the SHOW_SOFT_DELETES TableFlags bit makes
// RopGetContentsTable snapshot a folder's soft-deleted (Recoverable Items) messages
// instead of its live mail, so a native MAPI client's "Recover Deleted Items" sees the
// dumpster. It also proves the soft-deleted row's properties still materialize (the
// QueryRows path reads them from the object store by message id).
func TestContentsTableShowSoftDeletes(t *testing.T) {
	dir := t.TempDir()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	live := []byte("From: s@hermex.test\r\nSubject: LIVE-ROW\r\n\r\nbody\r\n")
	gone := []byte("From: s@hermex.test\r\nSubject: DUMPSTER-ROW\r\n\r\nbody\r\n")
	if _, err := st.AppendMessage(int64(mapi.PrivateFIDInbox), live, time.Now(), 0); err != nil {
		t.Fatal(err)
	}
	info, err := st.AppendMessage(int64(mapi.PrivateFIDInbox), gone, time.Now(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SoftDeleteMessage(int64(mapi.PrivateFIDInbox), info.UID); err != nil {
		t.Fatal(err)
	}
	st.Close()

	sess := NewSession(dir, nil, "")
	defer sess.Close()

	_, h := sess.Dispatch(logonRequest(0, 0x01), []uint32{0xFFFFFFFF})
	logonH := h[0]
	_, h = sess.Dispatch(buildOpenFolder(0, 1, uint64(mapi.MakeEIDEx(1, mapi.PrivateFIDInbox))), []uint32{logonH, 0xFFFFFFFF})
	folderH := h[1]

	// Without the flag the live contents table excludes the soft-deleted row.
	gctLive, _ := sess.Dispatch(buildGetContentsTable(0, 1), []uint32{folderH, 0xFFFFFFFF})
	if rc := contentsRowCount(t, gctLive); rc != 1 {
		t.Fatalf("live contents RowCount = %d, want 1 (only LIVE-ROW)", rc)
	}

	// With SHOW_SOFT_DELETES the table holds only the soft-deleted row.
	gctDel, h := sess.Dispatch(buildGetContentsTableFlags(0, 1, tableFlagSoftDeletes), []uint32{folderH, 0xFFFFFFFF})
	if rc := contentsRowCount(t, gctDel); rc != 1 {
		t.Fatalf("soft-deleted contents RowCount = %d, want 1 (only DUMPSTER-ROW)", rc)
	}
	tableH := h[1]

	cols := []mapi.PropTag{mapi.PrSubject}
	sess.Dispatch(buildSetColumns(0, cols), []uint32{tableH})
	qr, _ := sess.Dispatch(buildQueryRows(0, 0, 1, 32), []uint32{tableH})
	_, rows := queryRowsResponse(t, qr, cols)
	if len(rows) != 1 {
		t.Fatalf("soft-deleted QueryRows returned %d rows, want 1", len(rows))
	}
	if subj, _ := rows[0].Get(mapi.PrSubject); subj != "DUMPSTER-ROW" {
		t.Errorf("soft-deleted row subject = %v, want \"DUMPSTER-ROW\"", subj)
	}
}
