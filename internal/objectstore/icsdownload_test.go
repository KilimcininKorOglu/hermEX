package objectstore

import (
	"testing"

	"hermex/internal/ics"
	"hermex/internal/mapi"
)

// drainDownload runs a download context to completion through small chunks so
// element chunking and large-body tearing are exercised, reassembles the stream
// with the ics parser, and returns the flat element list.
func drainDownload(t *testing.T, dc *DownloadContext, chunkSize int) []ics.Item {
	t.Helper()
	var ps ics.Parser
	var items []ics.Item
	for {
		chunk, last, err := dc.GetBuffer(chunkSize)
		if err != nil {
			t.Fatalf("GetBuffer: %v", err)
		}
		ps.Feed(chunk)
		for {
			it, ok, err := ps.Next()
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if !ok {
				break
			}
			items = append(items, it)
		}
		if last {
			break
		}
	}
	return items
}

func countMarkers(items []ics.Item, marker uint32) int {
	n := 0
	for _, it := range items {
		if it.IsMarker && it.Marker == marker {
			n++
		}
	}
	return n
}

func hasProp(items []ics.Item, tag mapi.PropTag) bool {
	for _, it := range items {
		if !it.IsMarker && it.Prop != nil && it.Prop.Tag == tag {
			return true
		}
	}
	return false
}

// hasPropID matches by property id regardless of type, so a check survives the
// codec retagging PR_MESSAGE_CLASS to PT_STRING8 on the wire.
func hasPropID(items []ics.Item, propid uint16) bool {
	for _, it := range items {
		if !it.IsMarker && it.Prop != nil && uint16(uint32(it.Prop.Tag)>>16) == propid {
			return true
		}
	}
	return false
}

// downloadState returns an empty ContentsDown state seeded against the store's
// replica mapper, the form a client uploads before a contents sync.
func downloadState(t *testing.T, s *Store) *ics.State {
	t.Helper()
	m, err := s.ReplicaMapper()
	if err != nil {
		t.Fatal(err)
	}
	return ics.NewState(ics.ContentsDown, m)
}

// TestContentDownloadInitialSync drives an initial sync (empty client state):
// every message is a change. It asserts the stream's structure round-trips
// through the parser, one INCRSYNCCHG per message, each change header leading
// with PR_SOURCE_KEY, a trailing state block (with the seen change-number set)
// and INCRSYNCEND, exercised across small chunks so the producer's tearing and
// the parser's reassembly are both on the real path.
func TestContentDownloadInitialSync(t *testing.T) {
	s := openSeededStore(t)
	fld := int64(mapi.PrivateFIDContacts)
	mustCreateMessage(t, s, fld, contactMsg("Ada Lovelace"))
	mustCreateMessage(t, s, fld, contactMsg("Grace Hopper"))

	dc, err := s.NewContentDownload(fld, downloadState(t, s), SyncNormal|SyncReadState, 0, nil)
	mustNoErr(t, "new content download", err)
	items := drainDownload(t, dc, 48)

	wantEq(t, "INCRSYNCCHG count (one per new message)", countMarkers(items, ics.MarkerIncrSyncChg), 2)
	wantSourceKeyAfterEachChange(t, items)
	wantStreamProp(t, items, mapi.PrChangeKey, "change header PR_CHANGE_KEY")
	wantEq(t, "INCRSYNCSTATEBEGIN count", countMarkers(items, ics.MarkerIncrSyncStateBegin), 1)
	wantEq(t, "INCRSYNCSTATEEND count", countMarkers(items, ics.MarkerIncrSyncStateEnd), 1)
	wantStreamProp(t, items, metaTagCnsetSeen, "state block seen change-number set")
	wantStreamEnd(t, items)
}

// The state-block meta tags a download stream carries, by their wire values.
const (
	metaTagIdsetGiven1  = mapi.PropTag(0x40170102)
	metaTagCnsetSeen    = mapi.PropTag(0x67960102)
	metaTagIdsetDeleted = mapi.PropTag(0x67E50102)
)

// wantStreamProp fails when the stream carries no such property.
func wantStreamProp(t *testing.T, items []ics.Item, tag mapi.PropTag, label string) {
	t.Helper()
	if !hasProp(items, tag) {
		t.Errorf("stream is missing %s", label)
	}
}

// wantStreamEnd checks INCRSYNCEND is the final element.
func wantStreamEnd(t *testing.T, items []ics.Item) {
	t.Helper()
	if last := items[len(items)-1]; !last.IsMarker || last.Marker != ics.MarkerIncrSyncEnd {
		t.Errorf("stream does not end with INCRSYNCEND: %+v", last)
	}
}

// wantSourceKeyAfterEachChange asserts the element right after each INCRSYNCCHG
// begins the change header with a 22-byte source key, rather than asserting the
// property is present somewhere in the stream.
func wantSourceKeyAfterEachChange(t *testing.T, items []ics.Item) {
	t.Helper()
	for i, it := range items {
		if !it.IsMarker || it.Marker != ics.MarkerIncrSyncChg {
			continue
		}
		if i+1 >= len(items) || items[i+1].IsMarker || items[i+1].Prop.Tag != mapi.PrSourceKey {
			t.Fatalf("INCRSYNCCHG at %d not followed by PR_SOURCE_KEY", i)
		}
		sk, _ := items[i+1].Prop.Value.([]byte)
		wantEq(t, "source key length", len(sk), 22)
	}
}

// TestContentDownloadIncremental drives a second sync where the client already
// holds the first message (in given + its change number in seen): only the
// second message is a change, and a given MID the store never had is reported in
// the deletions set.
func TestContentDownloadIncremental(t *testing.T) {
	s := openSeededStore(t)
	fld := int64(mapi.PrivateFIDContacts)
	mid1, err := s.CreateMessage(fld, contactMsg("Ada Lovelace"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateMessage(fld, contactMsg("Grace Hopper")); err != nil {
		t.Fatal(err)
	}

	state := downloadState(t, s)
	state.Given().Append(mapi.MakeEIDEx(homeReplID, uint64(mid1)))
	state.Seen().AppendRange(homeReplID, 1, msgCN(t, s, mid1))
	phantom := uint64(mid1) + 1_000_000
	state.Given().Append(mapi.MakeEIDEx(homeReplID, phantom))

	dc, err := s.NewContentDownload(fld, state, SyncNormal, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	items := drainDownload(t, dc, 64)

	wantEq(t, "INCRSYNCCHG count (only the second message changed)", countMarkers(items, ics.MarkerIncrSyncChg), 1)
	wantEq(t, "INCRSYNCDEL count for the phantom given MID", countMarkers(items, ics.MarkerIncrSyncDel), 1)
	wantStreamProp(t, items, metaTagIdsetDeleted, "the deletions block's deleted id set")
}

// hierarchyState returns an empty HierarchyDown state against the store mapper.
func hierarchyState(t *testing.T, s *Store) *ics.State {
	t.Helper()
	m, err := s.ReplicaMapper()
	if err != nil {
		t.Fatal(err)
	}
	return ics.NewState(ics.HierarchyDown, m)
}

// TestHierarchyDownloadInitialSync drives an initial hierarchy sync over a
// controlled subtree: every subfolder is a change carrying its source key and
// display name, and the stream ends with a HierarchyDown state block (given +
// seen) and INCRSYNCEND.
func TestHierarchyDownloadInitialSync(t *testing.T) {
	s := openSeededStore(t)
	parent := mustCreateFolder(t, s, nil, "sync-root")
	mustCreateFolder(t, s, &parent, "A")
	mustCreateFolder(t, s, &parent, "B")

	dc, err := s.NewHierarchyDownload(parent, hierarchyState(t, s), 0, nil)
	mustNoErr(t, "new hierarchy download", err)
	items := drainDownload(t, dc, 40)

	wantEq(t, "INCRSYNCCHG count (one per subfolder)", countMarkers(items, ics.MarkerIncrSyncChg), 2)
	wantStreamProp(t, items, mapi.PrSourceKey, "folder change PR_SOURCE_KEY")
	wantStreamProp(t, items, mapi.PrDisplayName, "folder change PR_DISPLAY_NAME")
	wantStreamProp(t, items, metaTagIdsetGiven1, "state block given id set")
	wantStreamProp(t, items, metaTagCnsetSeen, "state block seen change-number set")
	wantStreamEnd(t, items)
}

// TestHierarchyDownloadIncremental drives a second hierarchy sync where the
// client holds the first subfolder: only the second is a change, and a given FID
// no longer present is reported deleted.
func TestHierarchyDownloadIncremental(t *testing.T) {
	s := openSeededStore(t)
	parent, err := s.CreateFolder(nil, "sync-root")
	if err != nil {
		t.Fatal(err)
	}
	fa, err := s.CreateFolder(&parent, "A")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateFolder(&parent, "B"); err != nil {
		t.Fatal(err)
	}

	state := hierarchyState(t, s)
	state.Given().Append(mapi.MakeEIDEx(homeReplID, uint64(fa)))
	state.Seen().AppendRange(homeReplID, 1, folderCN(t, s, fa))
	phantom := uint64(fa) + 1_000_000
	state.Given().Append(mapi.MakeEIDEx(homeReplID, phantom))

	dc, err := s.NewHierarchyDownload(parent, state, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	items := drainDownload(t, dc, 64)

	if n := countMarkers(items, ics.MarkerIncrSyncChg); n != 1 {
		t.Errorf("INCRSYNCCHG count = %d, want 1 (only the second subfolder changed)", n)
	}
	if countMarkers(items, ics.MarkerIncrSyncDel) != 1 {
		t.Error("expected an INCRSYNCDEL marker for the phantom given FID")
	}
	if !hasProp(items, mapi.PropTag(0x67E50102)) { // MetaTagIdsetDeleted
		t.Error("deletions block missing MetaTagIdsetDeleted")
	}
}

// TestContentDownloadPropertyFilter checks the exclusion filter: a message class
// excluded by the SyncConfigure proptag list does not appear in the body, while
// an unlisted property still does.
func TestContentDownloadPropertyFilter(t *testing.T) {
	s := openSeededStore(t)
	fld := int64(mapi.PrivateFIDContacts)
	if _, err := s.CreateMessage(fld, contactMsg("Ada Lovelace")); err != nil {
		t.Fatal(err)
	}

	dc, err := s.NewContentDownload(fld, downloadState(t, s), SyncNormal, 0, []mapi.PropTag{mapi.PrDisplayName})
	if err != nil {
		t.Fatal(err)
	}
	items := drainDownload(t, dc, 4096)

	if hasPropID(items, 0x3001) { // PR_DISPLAY_NAME id
		t.Error("excluded PR_DISPLAY_NAME still emitted in the body")
	}
	if !hasPropID(items, 0x001A) { // PR_MESSAGE_CLASS id (wire type is forced to PT_STRING8)
		t.Error("unlisted PR_MESSAGE_CLASS should survive the exclusion filter")
	}
}
