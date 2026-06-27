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
	if id := mustU8(t, p, "RopId"); id != ropGetPerUserLongTermIds {
		t.Fatalf("response 1 RopId = %#x, want %#x", id, ropGetPerUserLongTermIds)
	}
	mustU8(t, p, "hindex")
	if ec := mustU32(t, p, "ec"); ec != ecSuccess {
		t.Fatalf("GetPerUserLongTermIds ec = %#x, want ecSuccess", ec)
	}
	if n := mustU16(t, p, "count"); n != 0 {
		t.Errorf("LongTermIdCount = %d, want 0", n)
	}

	// 0x61 GetPerUserGuid: ecNotFound (a private logon holds no per-user guid).
	if id := mustU8(t, p, "RopId"); id != ropGetPerUserGuid {
		t.Fatalf("response 2 RopId = %#x, want %#x (batch misframed)", id, ropGetPerUserGuid)
	}
	mustU8(t, p, "hindex")
	if ec := mustU32(t, p, "ec"); ec != ecNotFound {
		t.Errorf("GetPerUserGuid ec = %#x, want ecNotFound %#x", ec, ecNotFound)
	}

	// 0x63 ReadPerUserInformation: ecSuccess + HasFinished + empty data.
	if id := mustU8(t, p, "RopId"); id != ropReadPerUserInformation {
		t.Fatalf("response 3 RopId = %#x, want %#x", id, ropReadPerUserInformation)
	}
	mustU8(t, p, "hindex")
	if ec := mustU32(t, p, "ec"); ec != ecSuccess {
		t.Fatalf("ReadPerUserInformation ec = %#x, want ecSuccess", ec)
	}
	if f := mustU8(t, p, "has_finished"); f != 1 {
		t.Errorf("HasFinished = %d, want 1", f)
	}
	if n := mustU16(t, p, "data size"); n != 0 {
		t.Errorf("DataSize = %d, want 0", n)
	}

	// 0x64 WritePerUserInformation (offset 0, with ReplGuid): ecSuccess.
	if id := mustU8(t, p, "RopId"); id != ropWritePerUserInformation {
		t.Fatalf("response 4 RopId = %#x, want %#x (read misframed)", id, ropWritePerUserInformation)
	}
	mustU8(t, p, "hindex")
	if ec := mustU32(t, p, "ec"); ec != ecSuccess {
		t.Fatalf("WritePerUserInformation(0) ec = %#x, want ecSuccess", ec)
	}

	// 0x64 WritePerUserInformation (offset 5, no ReplGuid): ecSuccess.
	if id := mustU8(t, p, "RopId"); id != ropWritePerUserInformation {
		t.Fatalf("response 5 RopId = %#x; the offset-0 write over/under-consumed its ReplGuid", id)
	}
	mustU8(t, p, "hindex")
	if ec := mustU32(t, p, "ec"); ec != ecSuccess {
		t.Fatalf("WritePerUserInformation(5) ec = %#x, want ecSuccess", ec)
	}

	// The chained no-op only frames if the final write consumed exactly.
	if id := mustU8(t, p, "tail RopId"); id != ropSetLocalReplicaMidsetDeleted {
		t.Fatalf("tail RopId = %#x; a per-user write misframed the batch", id)
	}
}
