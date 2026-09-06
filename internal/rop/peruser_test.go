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
// over a private logon, asserting their documented minimal responses and, by chaining,
// that each consumed its request exactly. The two writes (DataOffset 0 with a trailing
// ReplGuid, then a nonzero offset without one) exercise both sides of the conditional
// trailing field; a no-op ROP at the tail only frames if the final write consumed
// exactly, and the first write parsing after GetPerUserGuid's error response proves
// the batch never misframed.
func TestPerUserInformationFamily(t *testing.T) {
	sess, logonH, _ := copyToSession(t)
	defer sess.Close()

	var batch []byte
	batch = append(batch, buildGetPerUserLongTermIds(0)...)
	batch = append(batch, buildGetPerUserGuid(0)...)
	batch = append(batch, buildReadPerUserInformation(0)...)
	batch = append(batch, buildWritePerUserInformation(0, 0, true)...)
	batch = append(batch, buildWritePerUserInformation(0, 5, false)...)
	batch = append(batch, buildSetLocalReplicaMidsetDeleted(1)...)

	sr, _ := sess.Dispatch(batch, []uint32{logonH})
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

	// 0x64 WritePerUserInformation (offset 5, no ReplGuid): ecSuccess. Reaching it
	// at all proves the offset-0 write consumed its ReplGuid exactly.
	nextROP(t, p, ropWritePerUserInformation, ecSuccess, "WritePerUserInformation(5)")

	// The chained no-op only frames if the final write consumed exactly.
	if id := mustU8(t, p, "tail RopId"); id != ropSetLocalReplicaMidsetDeleted {
		t.Fatalf("tail RopId = %#x; a per-user write misframed the batch", id)
	}
}
