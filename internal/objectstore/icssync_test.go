package objectstore

import (
	"slices"
	"testing"

	"hermex/internal/ics"
	"hermex/internal/mapi"
)

// homeMapper binds the home replica id to a fixed GUID, the mapping the ics state
// mapper supplies in production. It lets a test drive the Serialize→Convert path
// the download path runs on a client's uploaded idset.
type homeMapper struct{ guid mapi.GUID }

func (m homeMapper) ToGUID(replid uint16) (mapi.GUID, bool) {
	if replid == homeReplID {
		return m.guid, true
	}
	return mapi.GUID{}, false
}

func (m homeMapper) ToID(g mapi.GUID) (uint16, bool) {
	if g == m.guid {
		return homeReplID, true
	}
	return 0, false
}

// looseSet builds a queryable home-replica idset from singleton GC values.
func looseSet(vs ...uint64) *ics.IDSet {
	s := ics.NewIDSet(ics.FormIDLoose, nil)
	for _, v := range vs {
		s.AppendRange(homeReplID, v, v)
	}
	return s
}

// allocCN hands out a change number via the real store allocator (faithful test
// data for the read_cn the read-state branch diffs, which no write path records
// yet).
func allocCN(t *testing.T, s *Store) uint64 {
	t.Helper()
	cn, err := allocateCN(s.objdb)
	if err != nil {
		t.Fatal(err)
	}
	return cn
}

// msgCN reads a message's stored change number.
func msgCN(t *testing.T, s *Store, mid int64) uint64 {
	t.Helper()
	var cn int64
	if err := s.objdb.QueryRow(`SELECT change_number FROM messages WHERE message_id=?`, mid).Scan(&cn); err != nil {
		t.Fatal(err)
	}
	return uint64(cn)
}

// folderCN reads a folder's stored change number.
func folderCN(t *testing.T, s *Store, fid int64) uint64 {
	t.Helper()
	var cn int64
	if err := s.objdb.QueryRow(`SELECT change_number FROM folders WHERE folder_id=?`, fid).Scan(&cn); err != nil {
		t.Fatal(err)
	}
	return uint64(cn)
}

func maxU64(xs ...uint64) uint64 {
	var m uint64
	for _, x := range xs {
		if x > m {
			m = x
		}
	}
	return m
}

// eqSet asserts an engine output (already sorted) equals the want set.
func eqSet(t *testing.T, name string, got []uint64, want ...uint64) {
	t.Helper()
	w := append([]uint64(nil), want...)
	slices.Sort(w)
	if !slices.Equal(got, w) {
		t.Errorf("%s = %v, want %v", name, got, w)
	}
}

// TestGetContentSync exercises every output branch of the contents diff in one
// scenario: an unchanged message, a content-changed one already in given, a
// brand-new one, a read-state-only change (read and unread), an FAI message
// filtered out by class gating (no-longer), and a given MID that no longer
// exists at all (deleted). The messages are created consecutively so their EIDs
// are E..E+5; dropping the new message from given leaves a two-value gap, so its
// given ranges stay split and it is correctly absent from the client's set.
func TestGetContentSync(t *testing.T) {
	s := openSeededStore(t)
	fld := int64(mapi.PrivateFIDContacts)

	mk := func(name string) int64 {
		id, err := s.CreateMessage(fld, contactMsg(name))
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	mUnchanged := mk("unchanged")
	mUpdated := mk("updated")
	mNew := mk("new")
	mRead := mk("read")
	mUnread := mk("unread")
	mFAI := mk("fai")

	// mFAI becomes folder-associated; the SYNC_NORMAL-only request gates it out.
	if _, err := s.objdb.Exec(`UPDATE messages SET is_associated=1 WHERE message_id=?`, mFAI); err != nil {
		t.Fatal(err)
	}
	// mRead/mUnread get a freshly allocated read change number — the version the
	// read-state branch compares against the client's Read set.
	rcnRead := allocCN(t, s)
	if _, err := s.objdb.Exec(`UPDATE messages SET read_state=1, read_cn=? WHERE message_id=?`, int64(rcnRead), mRead); err != nil {
		t.Fatal(err)
	}
	rcnUnread := allocCN(t, s)
	if _, err := s.objdb.Exec(`UPDATE messages SET read_state=0, read_cn=? WHERE message_id=?`, int64(rcnUnread), mUnread); err != nil {
		t.Fatal(err)
	}

	u := func(x int64) uint64 { return uint64(x) }
	phantom := u(mUnchanged) + 1_000_000 // a MID the client claims but the store never had

	given := looseSet(u(mUnchanged), u(mUpdated), u(mRead), u(mUnread), u(mFAI), phantom)
	seen := looseSet(msgCN(t, s, mUnchanged), msgCN(t, s, mRead), msgCN(t, s, mUnread))
	read := looseSet() // SYNC_READ_STATE enabled, but no read CN acknowledged yet

	res, err := s.GetContentSync(ContentSyncRequest{
		FolderID: fld, Given: given, Seen: seen, SeenFAI: nil, Read: read,
	})
	if err != nil {
		t.Fatal(err)
	}

	eqSet(t, "ChangedMIDs", res.ChangedMIDs, u(mUpdated), u(mNew))
	eqSet(t, "UpdatedMIDs", res.UpdatedMIDs, u(mUpdated))
	eqSet(t, "GivenMIDs", res.GivenMIDs, u(mUnchanged), u(mUpdated), u(mNew), u(mRead), u(mUnread))
	eqSet(t, "DeletedMIDs", res.DeletedMIDs, phantom)
	eqSet(t, "NoLongerMIDs", res.NoLongerMIDs, u(mFAI))
	eqSet(t, "ReadMIDs", res.ReadMIDs, u(mRead))
	eqSet(t, "UnreadMIDs", res.UnreadMIDs, u(mUnread))

	wantLastCN := maxU64(msgCN(t, s, mUnchanged), msgCN(t, s, mUpdated), msgCN(t, s, mNew),
		msgCN(t, s, mRead), msgCN(t, s, mUnread))
	if res.LastCN != wantLastCN {
		t.Errorf("LastCN = %d, want %d", res.LastCN, wantLastCN)
	}
	if res.LastReadCN != maxU64(rcnRead, rcnUnread) {
		t.Errorf("LastReadCN = %d, want %d", res.LastReadCN, maxU64(rcnRead, rcnUnread))
	}
}

// TestContentSyncNormalClassDisabled pins the symmetric gating contract: a nil
// Seen disables the normal class, so a normal message the client still holds is
// reported no-longer (out of scope), not kept.
func TestContentSyncNormalClassDisabled(t *testing.T) {
	s := openSeededStore(t)
	fld := int64(mapi.PrivateFIDContacts)
	m, err := s.CreateMessage(fld, contactMsg("normal"))
	if err != nil {
		t.Fatal(err)
	}
	res, err := s.GetContentSync(ContentSyncRequest{
		FolderID: fld, Given: looseSet(uint64(m)), Seen: nil, SeenFAI: looseSet(), Read: nil,
	})
	if err != nil {
		t.Fatal(err)
	}
	eqSet(t, "GivenMIDs", res.GivenMIDs)
	eqSet(t, "ChangedMIDs", res.ChangedMIDs)
	eqSet(t, "NoLongerMIDs", res.NoLongerMIDs, uint64(m))
}

// TestContentSyncGivenSurvivesGUIDConvert pins the replid-1 binding across the
// full download-path idset conversion: a client given/seen idset keyed by the
// store's replica GUID, serialized and read back as a packed GUID set, then
// Converted through the home mapper, must still resolve to replid 1 so the diff's
// MakeEIDEx(1, …) lookups hit. If that binding broke, every message would read
// as changed and a cached-mode client would full-resync on every cycle.
func TestContentSyncGivenSurvivesGUIDConvert(t *testing.T) {
	s := openSeededStore(t)
	fld := int64(mapi.PrivateFIDContacts)
	mid, err := s.CreateMessage(fld, contactMsg("x"))
	if err != nil {
		t.Fatal(err)
	}
	cn := msgCN(t, s, mid)

	guid, err := s.replicaGUID()
	if err != nil {
		t.Fatal(err)
	}
	m := homeMapper{guid: guid}

	convert := func(v uint64) *ics.IDSet {
		src := ics.NewIDSet(ics.FormGUIDLoose, m)
		src.AppendRange(homeReplID, v, v)
		wire, err := src.Serialize()
		if err != nil {
			t.Fatal(err)
		}
		packed := ics.NewIDSet(ics.FormGUIDPacked, m)
		if err := packed.Deserialize(wire); err != nil {
			t.Fatal(err)
		}
		if !packed.Convert() {
			t.Fatal("Convert failed to resolve the home replica GUID")
		}
		return packed
	}

	res, err := s.GetContentSync(ContentSyncRequest{
		FolderID: fld, Given: convert(uint64(mid)), Seen: convert(cn), SeenFAI: nil, Read: nil,
	})
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(res.ChangedMIDs, uint64(mid)) {
		t.Error("message keyed via the GUID Serialize→Convert path read as changed; the replid-1 binding broke")
	}
	if !slices.Contains(res.GivenMIDs, uint64(mid)) {
		t.Errorf("message %d missing from the keep set after GUID convert", mid)
	}
}

// TestGetHierarchySync checks the subfolder diff over a controlled subtree: an
// initial sync reports every subfolder as a change, and an incremental sync keeps
// an unchanged folder, reports a new one as changed, and reports a given FID that
// no longer exists as deleted.
func TestGetHierarchySync(t *testing.T) {
	s := openSeededStore(t)
	parent, err := s.CreateFolder(nil, "sync-root")
	if err != nil {
		t.Fatal(err)
	}
	fa, err := s.CreateFolder(&parent, "A")
	if err != nil {
		t.Fatal(err)
	}
	fb, err := s.CreateFolder(&parent, "B")
	if err != nil {
		t.Fatal(err)
	}

	res, err := s.GetHierarchySync(HierarchySyncRequest{FolderID: parent})
	if err != nil {
		t.Fatal(err)
	}
	eqSet(t, "initial ChangedFIDs", res.ChangedFIDs, uint64(fa), uint64(fb))
	eqSet(t, "initial GivenFIDs", res.GivenFIDs, uint64(fa), uint64(fb))
	eqSet(t, "initial DeletedFIDs", res.DeletedFIDs)

	phantom := uint64(fa) + 1_000_000
	res, err = s.GetHierarchySync(HierarchySyncRequest{
		FolderID: parent,
		Given:    looseSet(uint64(fa), phantom),
		Seen:     looseSet(folderCN(t, s, fa)),
	})
	if err != nil {
		t.Fatal(err)
	}
	eqSet(t, "ChangedFIDs", res.ChangedFIDs, uint64(fb))
	eqSet(t, "GivenFIDs", res.GivenFIDs, uint64(fa), uint64(fb))
	eqSet(t, "DeletedFIDs", res.DeletedFIDs, phantom)
}
