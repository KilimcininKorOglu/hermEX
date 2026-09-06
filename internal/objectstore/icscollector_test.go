package objectstore

import (
	"database/sql"
	"testing"

	"hermex/internal/ics"
	"hermex/internal/mapi"
)

// parseStream reassembles a FastTransfer byte stream into its flat element list.
func parseStream(t *testing.T, stream []byte) []ics.Item {
	t.Helper()
	var ps ics.Parser
	ps.Feed(stream)
	var items []ics.Item
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
	return items
}

// streamPropBytes returns the binary value carried under tag in a parsed stream.
func streamPropBytes(items []ics.Item, tag mapi.PropTag) ([]byte, bool) {
	for _, it := range items {
		if !it.IsMarker && it.Prop != nil && it.Prop.Tag == tag {
			b, ok := it.Prop.Value.([]byte)
			return b, ok
		}
	}
	return nil, false
}

// TestUploadStateStreamRoundTrip replays a prior seen set as a chunked state
// stream, then asserts GetTransferState frames it back exactly, INCRSYNCSTATEBEGIN
// first, INCRSYNCSTATEEND last, with the uploaded change-number range surviving the
// GUID-packed serialize/deserialize/convert cycle the wire imposes.
func TestUploadStateStreamRoundTrip(t *testing.T) {
	s := openSeededStore(t)
	m, err := s.ReplicaMapper()
	mustNoErr(t, "replica mapper", err)
	col := mustContentUpload(t, s, int64(mapi.PrivateFIDContacts))

	// The client uploads a prior seen set covering change numbers [1,20].
	src := ics.NewIDSet(ics.FormGUIDLoose, m)
	src.AppendRange(homeReplID, 1, 20)
	b, err := src.Serialize()
	mustNoErr(t, "serialize seen set", err)
	uploadStateStream(t, col, cnsetSeen, b[:1], b[1:])

	stream, err := col.GetTransferState()
	mustNoErr(t, "get transfer state", err)
	items := parseStream(t, stream)
	if len(items) == 0 || !items[0].IsMarker || items[0].Marker != ics.MarkerIncrSyncStateBegin {
		t.Fatalf("stream does not open with INCRSYNCSTATEBEGIN: %+v", items)
	}
	if last := items[len(items)-1]; !last.IsMarker || last.Marker != ics.MarkerIncrSyncStateEnd {
		t.Fatalf("stream does not end with INCRSYNCSTATEEND: %+v", last)
	}

	got := roundTrippedSet(t, m, items, cnsetSeen)
	wantEq(t, "round-tripped seen set holds a change number inside [1,20]",
		got.Contains(mapi.MakeEIDEx(homeReplID, 10)), true)
	wantEq(t, "round-tripped seen set holds a change number past [1,20]",
		got.Contains(mapi.MakeEIDEx(homeReplID, 21)), false)
}

// cnsetSeen is MetaTagCnsetSeen, the state-stream property carrying the seen
// change-number set.
const cnsetSeen = 0x67960102

// uploadStateStream replays a serialized id set as a state stream. Passing more
// than one chunk tears the buffer, which exercises reassembly across
// ContinueStateStream calls.
func uploadStateStream(t *testing.T, col *UploadCollector, tag uint32, chunks ...[]byte) {
	t.Helper()
	mustNoErr(t, "begin state stream", col.BeginStateStream(tag))
	for _, c := range chunks {
		mustNoErr(t, "continue state stream", col.ContinueStateStream(c))
	}
	mustNoErr(t, "end state stream", col.EndStateStream())
}

// mustContentUpload and mustHierarchyUpload open an upload collector.
func mustContentUpload(t *testing.T, s *Store, folderID int64) *UploadCollector {
	t.Helper()
	col, err := s.NewContentUpload(folderID)
	mustNoErr(t, "new content upload", err)
	return col
}

func mustHierarchyUpload(t *testing.T, s *Store, rootFID int64) *UploadCollector {
	t.Helper()
	col, err := s.NewHierarchyUpload(rootFID)
	mustNoErr(t, "new hierarchy upload", err)
	return col
}

// roundTrippedSet decodes an id set back out of a framed transfer state, through
// the same GUID-packed deserialize/convert cycle the wire imposes.
func roundTrippedSet(t *testing.T, m ics.ReplicaMapper, items []ics.Item, tag uint32) *ics.IDSet {
	t.Helper()
	raw, ok := streamPropBytes(items, mapi.PropTag(tag))
	if !ok {
		t.Fatalf("transfer state missing property %#x", tag)
	}
	got := ics.NewIDSet(ics.FormGUIDPacked, m)
	mustNoErr(t, "deserialize id set", got.Deserialize(raw))
	if !got.Convert() {
		t.Fatal("cannot resolve replicas for the round-tripped set")
	}
	return got
}

// TestUploadCollectorReadStateFeedsState asserts a read-state import folds its
// store-assigned read change number into the collector's read set, the only
// contents import that touches the upload state.
func TestUploadCollectorReadStateFeedsState(t *testing.T) {
	s := openSeededStore(t)
	fld := int64(mapi.PrivateFIDContacts)
	mid, err := s.CreateMessage(fld, richMsg("unread"))
	if err != nil {
		t.Fatal(err)
	}
	home, err := s.replicaGUID()
	if err != nil {
		t.Fatal(err)
	}
	col, err := s.NewContentUpload(fld)
	if err != nil {
		t.Fatal(err)
	}

	if err := col.ImportReadStateChanges([]ReadStateChange{
		{SourceKey: sourceKey(home, uint64(mid)), MarkRead: true},
	}); err != nil {
		t.Fatal(err)
	}

	var rcn sql.NullInt64
	if err := s.objdb.QueryRow(`SELECT read_cn FROM messages WHERE message_id=?`, mid).Scan(&rcn); err != nil {
		t.Fatal(err)
	}
	if !rcn.Valid {
		t.Fatal("read import did not record a read change number")
	}
	if !col.State().Read().Contains(mapi.MakeEIDEx(homeReplID, uint64(rcn.Int64))) {
		t.Errorf("read change number %d not folded into the collector read set", rcn.Int64)
	}
}

// TestUploadCollectorHierarchyFeedsState asserts a hierarchy import folds the new
// folder's change number into the collector's seen set.
func TestUploadCollectorHierarchyFeedsState(t *testing.T) {
	s := openSeededStore(t)
	root := int64(mapi.PrivateFIDIPMSubtree)
	col, err := s.NewHierarchyUpload(root)
	if err != nil {
		t.Fatal(err)
	}
	fid := uint64(0x200001)

	got, err := col.ImportHierarchyChange(hierHeader(t, s, fid, nil, "Imported"),
		mapi.PropertyValues{{Tag: mapi.PrContainerClass, Value: mapi.ContainerClassNote}})
	if err != nil {
		t.Fatal(err)
	}
	if got != fid {
		t.Fatalf("folder id = %#x, want %#x", got, fid)
	}
	cn := folderCN(t, s, int64(fid))
	if !col.State().Seen().Contains(mapi.MakeEIDEx(homeReplID, cn)) {
		t.Errorf("folder change number %d not folded into the collector seen set", cn)
	}
}

// TestUploadStateStreamDiscardsGiven asserts the upload path accepts the client's
// given set (protocol compliance) but never echoes it back: an importing context
// keeps no record of what the client already holds, while a seen set it must track
// does round-trip. Without the discard, the contents serialize would emit the
// given set straight back to the client.
func TestUploadStateStreamDiscardsGiven(t *testing.T) {
	s := openSeededStore(t)
	m, err := s.ReplicaMapper()
	mustNoErr(t, "replica mapper", err)
	col := mustContentUpload(t, s, int64(mapi.PrivateFIDContacts))

	uploadStateStream(t, col, idsetGiven1, serializedRange(t, m, 100, 200))
	uploadStateStream(t, col, cnsetSeen, serializedRange(t, m, 1, 20))

	items := parseStream(t, mustTransferState(t, col))
	_, echoedGiven := streamPropBytes(items, mapi.PropTag(idsetGiven1))
	wantEq(t, "upload transfer state echoed the client given set (it must be discarded)", echoedGiven, false)
	_, keptSeen := streamPropBytes(items, mapi.PropTag(cnsetSeen))
	wantEq(t, "upload transfer state kept the tracked seen set", keptSeen, true)
}

// idsetGiven1 is MetaTagIdsetGiven1, the state-stream property carrying the set
// of ids the client already holds.
const idsetGiven1 = 0x40170102

// serializedRange builds a loose-form id set over one change-number range and
// returns its wire bytes.
func serializedRange(t *testing.T, m ics.ReplicaMapper, lo, hi uint64) []byte {
	t.Helper()
	set := ics.NewIDSet(ics.FormGUIDLoose, m)
	set.AppendRange(homeReplID, lo, hi)
	b, err := set.Serialize()
	mustNoErr(t, "serialize id set", err)
	return b
}

// TestUploadStateStreamHierarchyRejectsContentsOnly asserts a hierarchy upload
// rejects the contents-only sets (cnset-seen-fai / cnset-read) while still
// accepting the seen set that applies to every sync type.
func TestUploadStateStreamHierarchyRejectsContentsOnly(t *testing.T) {
	s := openSeededStore(t)
	hcol := mustHierarchyUpload(t, s, int64(mapi.PrivateFIDIPMSubtree))
	const (
		cnsetSeenFAI = 0x67DA0102
		cnsetRead    = 0x67D20102
	)
	wantErr(t, "hierarchy upload accepting a contents-only FAI seen set", hcol.BeginStateStream(cnsetSeenFAI))
	wantErr(t, "hierarchy upload accepting a contents-only read set", hcol.BeginStateStream(cnsetRead))
	mustNoErr(t, "hierarchy upload rejected the seen set valid for all sync types", hcol.BeginStateStream(cnsetSeen))
}

// mustTransferState renders the collector's transfer state or fails the test.
func mustTransferState(t *testing.T, col *UploadCollector) []byte {
	t.Helper()
	stream, err := col.GetTransferState()
	if err != nil {
		t.Fatal(err)
	}
	return stream
}

// TestUploadStateStreamGate covers the state-stream guards: a non-state meta-tag is
// rejected, the mark-started gate blocks a state stream opened after an import, and
// a continue/end with no open stream is an error rather than a silent no-op.
func TestUploadStateStreamGate(t *testing.T) {
	s := openSeededStore(t)
	col := mustContentUpload(t, s, int64(mapi.PrivateFIDContacts))

	wantErr(t, "BeginStateStream accepting a non-state meta-tag", col.BeginStateStream(uint32(mapi.PrDisplayName)))
	wantErr(t, "ContinueStateStream accepting bytes with no open stream", col.ContinueStateStream([]byte{0}))
	wantErr(t, "EndStateStream accepting a close with no open stream", col.EndStateStream())

	// An import trips the mark-started gate: no further state may be replayed.
	hcol := mustHierarchyUpload(t, s, int64(mapi.PrivateFIDIPMSubtree))
	mustImportHierarchyChange(t, s, hcol, 0x200002, "Gate")
	wantErr(t, "BeginStateStream after an import (mark-started gate not enforced)", hcol.BeginStateStream(cnsetSeen))

	// A stream still open when an import runs can no longer be continued or ended.
	ocol := mustHierarchyUpload(t, s, int64(mapi.PrivateFIDIPMSubtree))
	mustNoErr(t, "begin state stream", ocol.BeginStateStream(cnsetSeen))
	mustImportHierarchyChange(t, s, ocol, 0x200003, "Open")
	wantErr(t, "ContinueStateStream after an import", ocol.ContinueStateStream([]byte{0}))
	wantErr(t, "EndStateStream after an import", ocol.EndStateStream())
}

// mustImportHierarchyChange imports one folder into a hierarchy upload.
func mustImportHierarchyChange(t *testing.T, s *Store, col *UploadCollector, fid uint64, name string) {
	t.Helper()
	_, err := col.ImportHierarchyChange(hierHeader(t, s, fid, nil, name), nil)
	mustNoErr(t, "import hierarchy change", err)
}
