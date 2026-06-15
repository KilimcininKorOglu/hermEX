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
// stream, then asserts GetTransferState frames it back exactly — INCRSYNCSTATEBEGIN
// first, INCRSYNCSTATEEND last, with the uploaded change-number range surviving the
// GUID-packed serialize/deserialize/convert cycle the wire imposes.
func TestUploadStateStreamRoundTrip(t *testing.T) {
	s := openSeededStore(t)
	m, err := s.ReplicaMapper()
	if err != nil {
		t.Fatal(err)
	}
	col, err := s.NewContentUpload(int64(mapi.PrivateFIDContacts))
	if err != nil {
		t.Fatal(err)
	}

	// The client uploads a prior seen set covering change numbers [1,20].
	src := ics.NewIDSet(ics.FormGUIDLoose, m)
	src.AppendRange(homeReplID, 1, 20)
	b, err := src.Serialize()
	if err != nil {
		t.Fatal(err)
	}
	const cnsetSeen = 0x67960102
	if err := col.BeginStateStream(cnsetSeen); err != nil {
		t.Fatal(err)
	}
	// Tear the buffer so reassembly across ContinueStateStream calls is exercised.
	if err := col.ContinueStateStream(b[:1]); err != nil {
		t.Fatal(err)
	}
	if err := col.ContinueStateStream(b[1:]); err != nil {
		t.Fatal(err)
	}
	if err := col.EndStateStream(); err != nil {
		t.Fatal(err)
	}

	stream, err := col.GetTransferState()
	if err != nil {
		t.Fatal(err)
	}
	items := parseStream(t, stream)
	if len(items) == 0 || !items[0].IsMarker || items[0].Marker != ics.MarkerIncrSyncStateBegin {
		t.Fatalf("stream does not open with INCRSYNCSTATEBEGIN: %+v", items)
	}
	if last := items[len(items)-1]; !last.IsMarker || last.Marker != ics.MarkerIncrSyncStateEnd {
		t.Fatalf("stream does not end with INCRSYNCSTATEEND: %+v", last)
	}

	seenBytes, ok := streamPropBytes(items, mapi.PropTag(cnsetSeen))
	if !ok {
		t.Fatal("transfer state missing the seen change-number set")
	}
	got := ics.NewIDSet(ics.FormGUIDPacked, m)
	if err := got.Deserialize(seenBytes); err != nil {
		t.Fatal(err)
	}
	if !got.Convert() {
		t.Fatal("cannot resolve replicas for the round-tripped seen set")
	}
	if !got.Contains(mapi.MakeEIDEx(homeReplID, 10)) {
		t.Error("round-tripped seen set lost a change number inside [1,20]")
	}
	if got.Contains(mapi.MakeEIDEx(homeReplID, 21)) {
		t.Error("round-tripped seen set gained a change number past [1,20]")
	}
}

// TestUploadCollectorReadStateFeedsState asserts a read-state import folds its
// store-assigned read change number into the collector's read set — the only
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

// TestUploadStateStreamGate covers the state-stream guards: a non-state meta-tag is
// rejected, the mark-started gate blocks a state stream opened after an import, and
// a continue/end with no open stream is an error rather than a silent no-op.
func TestUploadStateStreamGate(t *testing.T) {
	s := openSeededStore(t)
	col, err := s.NewContentUpload(int64(mapi.PrivateFIDContacts))
	if err != nil {
		t.Fatal(err)
	}

	if err := col.BeginStateStream(uint32(mapi.PrDisplayName)); err == nil {
		t.Error("BeginStateStream accepted a non-state meta-tag")
	}
	if err := col.ContinueStateStream([]byte{0}); err == nil {
		t.Error("ContinueStateStream accepted bytes with no open stream")
	}
	if err := col.EndStateStream(); err == nil {
		t.Error("EndStateStream accepted a close with no open stream")
	}

	// An import trips the mark-started gate: no further state may be replayed.
	hcol, err := s.NewHierarchyUpload(int64(mapi.PrivateFIDIPMSubtree))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := hcol.ImportHierarchyChange(hierHeader(t, s, 0x200002, nil, "Gate"), nil); err != nil {
		t.Fatal(err)
	}
	if err := hcol.BeginStateStream(0x67960102); err == nil {
		t.Error("BeginStateStream succeeded after an import (mark-started gate not enforced)")
	}
}
