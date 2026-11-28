package rop

import (
	"testing"

	"hermex/internal/ext"
)

func buildGetPerUserLongTermIds(inIdx uint8) []byte {
	b := ext.NewPush(ext.FlagUTF16)
	b.Uint8(ropGetPerUserLongTermIds)
	b.Uint8(0) // LogonId
	b.Uint8(inIdx)
	b.Raw(make([]byte, 16)) // DatabaseGuid
	return b.Bytes()
}

func buildGetPerUserGuid(inIdx uint8) []byte {
	b := ext.NewPush(ext.FlagUTF16)
	b.Uint8(ropGetPerUserGuid)
	b.Uint8(0)
	b.Uint8(inIdx)
	b.Raw(make([]byte, 24)) // LongTermId
	return b.Bytes()
}

func buildReadPerUserInformation(inIdx uint8) []byte {
	b := ext.NewPush(ext.FlagUTF16)
	b.Uint8(ropReadPerUserInformation)
	b.Uint8(0)
	b.Uint8(inIdx)
	b.Raw(make([]byte, 24)) // FolderId (LongTermId)
	b.Uint8(0)              // Reserved
	b.Uint32(0)             // DataOffset
	b.Uint16(0x1000)        // MaxDataSize
	return b.Bytes()
}

// buildWritePerUserInformation frames a write whose trailing ReplGuid is present only
// for a first chunk (DataOffset 0) on a private logon, matching the conditional field.
func buildWritePerUserInformation(inIdx uint8, offset uint32, withGUID bool) []byte {
	b := ext.NewPush(ext.FlagUTF16)
	b.Uint8(ropWritePerUserInformation)
	b.Uint8(0)
	b.Uint8(inIdx)
	b.Raw(make([]byte, 24)) // FolderId (LongTermId)
	b.Uint8(0)              // HasFinished
	b.Uint32(offset)
	b.Uint16(2)         // Data length
	b.Raw([]byte{1, 2}) // Data
	if withGUID {
		b.Raw(make([]byte, 16)) // ReplGuid
	}
	return b.Bytes()
}

// TestPerUserInformationFamily drives all four per-user-information ROPs in one batch
// over a private logon, asserting their documented minimal responses, handle error
// refusals, truncated DataOffset=0 ReplGuid rejection, and exact batch framing.
func TestPerUserInformationFamily(t *testing.T) {
	sess := NewSession(provisionedDir(t), nil, "")
	defer sess.Close()
	logonH := dispatchOK(t, sess, logonRequest(0, 0x01), ropLogon, "Logon", 0xFFFFFFFF)[0]

	var batch []byte
	batch = append(batch, buildGetPerUserLongTermIds(0)...)
	batch = append(batch, buildGetPerUserGuid(0)...)
	batch = append(batch, buildReadPerUserInformation(0)...)
	batch = append(batch, buildWritePerUserInformation(0, 0, true)...)
	batch = append(batch, buildWritePerUserInformation(0, 5, false)...)
	batch = append(batch, buildSetLocalReplicaMidsetDeleted(1)...)

	sr, outHandles := sess.Dispatch(batch, []uint32{logonH})
	if len(outHandles) != 1 || outHandles[0] != logonH {
		t.Fatalf("handles after per-user batch = %#x, want [%#x]", outHandles, logonH)
	}
	p := ext.NewPull(sr, ext.FlagUTF16)

	// 0x60 GetPerUserLongTermIds: ecSuccess + empty LongTermId array.
	nextROP(t, p, ropGetPerUserLongTermIds, ecSuccess, "GetPerUserLongTermIds")
	wantU16(t, p, "LongTermIdCount", 0)

	// 0x61 GetPerUserGuid: ecNotFound (a private logon holds no per-user guid).
	nextROP(t, p, ropGetPerUserGuid, ecNotFound, "GetPerUserGuid")

	// 0x63 ReadPerUserInformation: ecSuccess + HasFinished + empty data.
	nextROP(t, p, ropReadPerUserInformation, ecSuccess, "ReadPerUserInformation")
	wantU8(t, p, "HasFinished", 1)
	wantU16(t, p, "DataSize", 0)

	// 0x64 WritePerUserInformation (offset 0, with ReplGuid): ecSuccess.
	nextROP(t, p, ropWritePerUserInformation, ecSuccess, "WritePerUserInformation(0)")

	// 0x64 WritePerUserInformation (offset 5, no ReplGuid): ecSuccess.
	nextROP(t, p, ropWritePerUserInformation, ecSuccess, "WritePerUserInformation(5)")

	// The chained no-op only frames if the final write consumed exactly.
	if id := mustU8(t, p, "tail RopId"); id != ropSetLocalReplicaMidsetDeleted {
		t.Fatalf("tail RopId = %#x; a per-user write misframed the batch", id)
	}

	// Hostile inputs: null handle returns ecError for each op, and DataOffset 0
	// missing its trailing ReplGuid aborts cleanly while keeping the logon handle intact.
	wantEC(t, dispatchOn(sess, buildGetPerUserLongTermIds(0), 0xFFFFFFFF), ropGetPerUserLongTermIds, ecError, "GetPerUserLongTermIds bad handle")
	wantEC(t, dispatchOn(sess, buildGetPerUserGuid(0), 0xFFFFFFFF), ropGetPerUserGuid, ecError, "GetPerUserGuid bad handle")
	wantEC(t, dispatchOn(sess, buildReadPerUserInformation(0), 0xFFFFFFFF), ropReadPerUserInformation, ecError, "ReadPerUserInformation bad handle")
	wantEC(t, dispatchOn(sess, buildWritePerUserInformation(0, 0, true), 0xFFFFFFFF), ropWritePerUserInformation, ecError, "WritePerUserInformation bad handle")

	truncResp, truncHandles := sess.Dispatch(buildWritePerUserInformation(0, 0, false), []uint32{logonH})
	if len(truncResp) != 0 || len(truncHandles) != 1 || truncHandles[0] != logonH {
		t.Errorf("truncated write = (%x, %#x), want empty response and unchanged handle [%#x]", truncResp, truncHandles, logonH)
	}
	wantEC(t, dispatchOn(sess, buildGetPerUserGuid(0), logonH), ropGetPerUserGuid, ecNotFound, "logon usable after truncated write")
}
