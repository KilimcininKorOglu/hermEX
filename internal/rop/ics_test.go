package rop

import (
	"testing"

	"hermex/internal/ext"
	"hermex/internal/ics"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

func buildSyncConfigure(inIdx, outIdx, syncType uint8, syncFlags uint16, extraFlags uint32, propTags []mapi.PropTag) []byte {
	b := ext.NewPush(ext.FlagUTF16)
	b.Uint8(ropSynchronizationConfigure)
	b.Uint8(0) // LogonId
	b.Uint8(inIdx)
	b.Uint8(outIdx)
	b.Uint8(syncType)
	b.Uint8(0) // SendOptions
	b.Uint16(syncFlags)
	b.Uint16(0) // RestrictionSize
	b.Uint32(extraFlags)
	_ = b.PropTags(propTags)
	return b.Bytes()
}

func buildGetBuffer(inIdx uint8, bufferSize uint16) []byte {
	b := ext.NewPush(ext.FlagUTF16)
	b.Uint8(ropFastTransferSourceGetBuffer)
	b.Uint8(0)
	b.Uint8(inIdx)
	b.Uint16(bufferSize)
	return b.Bytes()
}

func buildStateStreamBegin(inIdx uint8, stateProp uint32) []byte {
	b := ext.NewPush(ext.FlagUTF16)
	b.Uint8(ropSyncUploadStateStreamBegin)
	b.Uint8(0)
	b.Uint8(inIdx)
	b.Uint32(stateProp)
	b.Uint32(0) // BufferSize (informational)
	return b.Bytes()
}

func buildStateStreamContinue(inIdx uint8, data []byte) []byte {
	b := ext.NewPush(ext.FlagUTF16)
	b.Uint8(ropSyncUploadStateStreamContinue)
	b.Uint8(0)
	b.Uint8(inIdx)
	b.BinEx(data)
	return b.Bytes()
}

func buildStateStreamEnd(inIdx uint8) []byte {
	b := ext.NewPush(ext.FlagUTF16)
	b.Uint8(ropSyncUploadStateStreamEnd)
	b.Uint8(0)
	b.Uint8(inIdx)
	return b.Bytes()
}

// drainSyncDownload sends GetBuffer repeatedly on the sync handle slot, asserts
// each response head, and reassembles the FastTransfer stream into parsed items.
func drainSyncDownload(t *testing.T, sess *Session, handles []uint32, syncIdx uint8) []ics.Item {
	t.Helper()
	var stream []byte
	for range 1000 {
		sr, _ := sess.Dispatch(buildGetBuffer(syncIdx, 0x1000), handles)
		p := ext.NewPull(sr, ext.FlagUTF16)
		if id := mustU8(t, p, "RopId"); id != ropFastTransferSourceGetBuffer {
			t.Fatalf("GetBuffer RopId = %#x", id)
		}
		mustU8(t, p, "hindex")
		if ec := mustU32(t, p, "ec"); ec != ecSuccess {
			t.Fatalf("GetBuffer ReturnValue = %#x", ec)
		}
		status := mustU16(t, p, "transfer_status")
		mustU16(t, p, "in_progress_count")
		mustU16(t, p, "total_step_count")
		mustU8(t, p, "reserved")
		data, err := p.BinShort()
		if err != nil {
			t.Fatalf("GetBuffer transfer_data: %v", err)
		}
		stream = append(stream, data...)
		if status == transferStatusError {
			t.Fatalf("GetBuffer reported transfer_status ERROR")
		}
		if status == transferStatusDone {
			var ps ics.Parser
			ps.Feed(stream)
			var items []ics.Item
			for {
				it, ok, err := ps.Next()
				if err != nil {
					t.Fatalf("parse assembled stream: %v", err)
				}
				if !ok {
					break
				}
				items = append(items, it)
			}
			return items
		}
	}
	t.Fatal("GetBuffer never reported DONE")
	return nil
}

func ropMarkerCount(items []ics.Item, marker uint32) int {
	n := 0
	for _, it := range items {
		if it.IsMarker && it.Marker == marker {
			n++
		}
	}
	return n
}

// configureInboxSync logs on, opens the inbox, and configures a contents-sync
// download on it, returning the live handle array (sync context at slot 2).
func configureInboxSync(t *testing.T, sess *Session, syncFlags uint16) []uint32 {
	t.Helper()
	_, h := sess.Dispatch(logonRequest(0, 0x01), []uint32{0xFFFFFFFF})
	logonH := h[0]
	inboxEID := uint64(mapi.MakeEIDEx(1, mapi.PrivateFIDInbox))
	_, h = sess.Dispatch(buildOpenFolder(0, 1, inboxEID), []uint32{logonH, 0xFFFFFFFF})
	folderH := h[1]
	handles := []uint32{logonH, folderH, 0xFFFFFFFF}
	sr, h := sess.Dispatch(buildSyncConfigure(1, 2, objectstore.SyncTypeContents, syncFlags, 0, nil), handles)
	p := ext.NewPull(sr, ext.FlagUTF16)
	if id := mustU8(t, p, "RopId"); id != ropSynchronizationConfigure {
		t.Fatalf("SyncConfigure RopId = %#x", id)
	}
	mustU8(t, p, "ohindex")
	if ec := mustU32(t, p, "ec"); ec != ecSuccess {
		t.Fatalf("SyncConfigure ReturnValue = %#x", ec)
	}
	return h
}

// TestSyncDownloadContents drives the full ICS download path through the ROP
// dispatch: logon, open inbox, SyncConfigure, then GetBuffer to completion. It
// asserts the reassembled stream carries one change per seeded message, a state
// block, and the terminating INCRSYNCEND — proving SyncConfigure + GetBuffer wire
// the existing download context end to end.
func TestSyncDownloadContents(t *testing.T) {
	dir := t.TempDir()
	seedInboxMessage(t, dir, "Ada Lovelace")
	seedInboxMessage(t, dir, "Grace Hopper")

	sess := NewSession(dir, nil, "")
	defer sess.Close()
	handles := configureInboxSync(t, sess, objectstore.SyncNormal|objectstore.SyncReadState)
	items := drainSyncDownload(t, sess, handles, 2)

	if n := ropMarkerCount(items, ics.MarkerIncrSyncChg); n != 2 {
		t.Errorf("INCRSYNCCHG count = %d, want 2 (one per seeded message)", n)
	}
	if ropMarkerCount(items, ics.MarkerIncrSyncStateBegin) != 1 || ropMarkerCount(items, ics.MarkerIncrSyncStateEnd) != 1 {
		t.Error("assembled stream missing its single state block")
	}
	if len(items) == 0 {
		t.Fatal("empty stream")
	}
	if last := items[len(items)-1]; !last.IsMarker || last.Marker != ics.MarkerIncrSyncEnd {
		t.Errorf("stream does not end with INCRSYNCEND: %+v", last)
	}
}

// TestSyncUploadStateStreamThenDownload exercises the state-stream ROPs: after
// SyncConfigure the client replays a (here empty) seen set via Begin/Continue/End,
// then drains. It proves the three state-stream opcodes are accepted on a sync
// handle and the subsequent download still completes.
func TestSyncUploadStateStreamThenDownload(t *testing.T) {
	dir := t.TempDir()
	seedInboxMessage(t, dir, "Katherine Johnson")

	sess := NewSession(dir, nil, "")
	defer sess.Close()
	handles := configureInboxSync(t, sess, objectstore.SyncNormal)

	// Replay an empty seen set (an initial-sync checkpoint) through the three ROPs.
	const cnsetSeen = 0x67960102
	for _, req := range [][]byte{
		buildStateStreamBegin(2, cnsetSeen),
		buildStateStreamContinue(2, nil),
		buildStateStreamEnd(2),
	} {
		sr, _ := sess.Dispatch(req, handles)
		p := ext.NewPull(sr, ext.FlagUTF16)
		mustU8(t, p, "RopId")
		mustU8(t, p, "hindex")
		if ec := mustU32(t, p, "ec"); ec != ecSuccess {
			t.Fatalf("state-stream ROP ReturnValue = %#x", ec)
		}
	}

	items := drainSyncDownload(t, sess, handles, 2)
	if n := ropMarkerCount(items, ics.MarkerIncrSyncChg); n != 1 {
		t.Errorf("INCRSYNCCHG count = %d, want 1", n)
	}
}
