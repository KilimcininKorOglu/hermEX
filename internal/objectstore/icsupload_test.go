package objectstore

import (
	"bytes"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"hermex/internal/ics"
	"hermex/internal/mapi"
	"hermex/internal/oxcmail"
)

// richMsg is a normal message exercising every part of the content stream: a
// top-level property bag, one recipient bag, and one attachment bag carrying
// binary payload (offloaded to a content file, so its round-trip crosses that
// boundary too).
func richMsg(subject string) *oxcmail.Message {
	return &oxcmail.Message{
		Props: mapi.PropertyValues{
			{Tag: mapi.PrMessageClass, Value: "IPM.Note"},
			{Tag: mapi.PrSubject, Value: subject},
			{Tag: mapi.PrBody, Value: "Body of " + subject},
			{Tag: mapi.PrImportance, Value: int32(1)},
		},
		Recipients: []mapi.PropertyValues{{
			{Tag: mapi.PrRecipientType, Value: int32(mapi.RecipTo)},
			{Tag: mapi.PrDisplayName, Value: "Alice"},
			{Tag: mapi.PrEmailAddress, Value: "alice@example.com"},
		}},
		Attachments: []oxcmail.Attachment{{Props: mapi.PropertyValues{
			{Tag: mapi.PrAttachMethod, Value: int32(mapi.AttachByValue)},
			{Tag: mapi.PrAttachLongFilename, Value: "note.txt"},
			{Tag: mapi.PrAttachDataBin, Value: []byte("attachment payload bytes")},
		}}},
	}
}

// propItem wraps a tagged property as a stream element.
func propItem(tag mapi.PropTag, val any) ics.Item {
	return ics.Item{Prop: &ics.StreamProp{Tag: tag, Value: val}}
}

// namedItem wraps a named property (its inline name) as a stream element.
func namedItem(propid uint16, typ mapi.PropType, name *mapi.PropertyName, val any) ics.Item {
	return ics.Item{Prop: &ics.StreamProp{
		Tag:   mapi.PropTag(uint32(propid)<<16 | uint32(typ)),
		Name:  name,
		Value: val,
	}}
}

// encodeStream serializes elements into FastTransfer bytes the way a client would
// upload them, so a test can feed a hand-built content body to the collector.
func encodeStream(t *testing.T, items ...ics.Item) []byte {
	t.Helper()
	var p ics.Producer
	for _, it := range items {
		if it.IsMarker {
			p.WriteMarker(it.Marker)
			continue
		}
		if err := p.WriteProp(*it.Prop); err != nil {
			t.Fatalf("encode stream prop %s: %v", it.Prop.Tag, err)
		}
	}
	return drainProducer(&p)
}

// drainProducer reads an entire producer queue into one buffer.
func drainProducer(p *ics.Producer) []byte {
	var out []byte
	for {
		chunk, last := p.ReadBuffer(1 << 20)
		out = append(out, chunk...)
		if last {
			return out
		}
	}
}

// importHeader builds the four-property import-change header for a home-replica
// message id. The change key and predecessor list are well-formed but unused by
// the store, which derives both from the change number on download.
func importHeader(t *testing.T, s *Store, mid uint64) mapi.PropertyValues {
	t.Helper()
	home, err := s.replicaGUID()
	if err != nil {
		t.Fatal(err)
	}
	ck, err := changeKey(home, 1)
	if err != nil {
		t.Fatal(err)
	}
	pcl, err := predecessorChangeList(home, 1)
	if err != nil {
		t.Fatal(err)
	}
	return mapi.PropertyValues{
		{Tag: mapi.PrSourceKey, Value: sourceKey(home, mid)},
		{Tag: mapi.PrLastModificationTime, Value: mapi.UnixToNTTime(time.Now())},
		{Tag: mapi.PrChangeKey, Value: ck},
		{Tag: mapi.PrPredecessorChangeList, Value: pcl},
	}
}

// importOne resolves, fills, and commits one message from a content body.
func importOne(t *testing.T, s *Store, folderID int64, mid uint64, flags uint8, body []byte) uint64 {
	t.Helper()
	um, err := s.ImportMessageChange(folderID, flags, importHeader(t, s, mid))
	if err != nil {
		t.Fatalf("ImportMessageChange: %v", err)
	}
	col := NewMessageCollector(um)
	if err := col.PutBuffer(body); err != nil {
		t.Fatalf("PutBuffer: %v", err)
	}
	id, err := um.Commit()
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	return id
}

// uploadDownloadStream replays a download stream the way a client uploads its
// changes back: each INCRSYNCCHG's header becomes an import-change call, the
// INCRSYNCMESSAGE body is re-framed (markers/props unchanged) and fed to the
// collector in fixed-size chunks, and the message is committed. It returns the
// reconstructed message ids.
func uploadDownloadStream(t *testing.T, dst *Store, folderID int64, items []ics.Item, chunk int) []uint64 {
	t.Helper()
	var mids []uint64
	for i := 0; i < len(items); {
		if !items[i].IsMarker || items[i].Marker != ics.MarkerIncrSyncChg {
			i++
			continue
		}
		i++ // past INCRSYNCCHG
		var header mapi.PropertyValues
		for i < len(items) && !(items[i].IsMarker && items[i].Marker == ics.MarkerIncrSyncMessage) {
			if items[i].IsMarker {
				t.Fatalf("unexpected marker %#x in change header", items[i].Marker)
			}
			header = append(header, mapi.TaggedPropVal{Tag: items[i].Prop.Tag, Value: items[i].Prop.Value})
			i++
		}
		if i >= len(items) {
			t.Fatal("change header not terminated by INCRSYNCMESSAGE")
		}
		i++ // past INCRSYNCMESSAGE
		var body ics.Producer
		for i < len(items) && !sectionBoundary(items[i]) {
			if items[i].IsMarker {
				body.WriteMarker(items[i].Marker)
			} else if err := body.WriteProp(*items[i].Prop); err != nil {
				t.Fatalf("re-encode body prop: %v", err)
			}
			i++
		}
		var flags uint8
		if v, ok := header.Get(mapi.PrAssociated); ok {
			if b, _ := v.(bool); b {
				flags |= ImportFlagAssociated
			}
		}
		um, err := dst.ImportMessageChange(folderID, flags, header)
		if err != nil {
			t.Fatalf("ImportMessageChange: %v", err)
		}
		col := NewMessageCollector(um)
		raw := drainProducer(&body)
		for off := 0; off < len(raw); off += chunk {
			end := min(off+chunk, len(raw))
			if err := col.PutBuffer(raw[off:end]); err != nil {
				t.Fatalf("PutBuffer: %v", err)
			}
		}
		id, err := um.Commit()
		if err != nil {
			t.Fatalf("Commit: %v", err)
		}
		mids = append(mids, id)
	}
	return mids
}

// sectionBoundary reports the markers that end a message body in a download
// stream: the next change, the deletions/read-state blocks, the state block, or
// the stream terminator.
func sectionBoundary(it ics.Item) bool {
	if !it.IsMarker {
		return false
	}
	switch it.Marker {
	case ics.MarkerIncrSyncChg, ics.MarkerIncrSyncDel, ics.MarkerIncrSyncRead,
		ics.MarkerIncrSyncStateBegin, ics.MarkerIncrSyncEnd:
		return true
	}
	return false
}

// valuesEqual compares two property values, treating binary values by content.
func valuesEqual(a, b any) bool {
	if ab, ok := a.([]byte); ok {
		bb, ok := b.([]byte)
		return ok && bytes.Equal(ab, bb)
	}
	return a == b
}

// assertPropsEqual compares two property bags by property id and value, tolerating
// the string-type normalization the FastTransfer wire applies (PT_STRING8 bodies
// arrive as PT_UNICODE), which changes a tag's type but never its id or value.
func assertPropsEqual(t *testing.T, ctx string, want, got mapi.PropertyValues) {
	t.Helper()
	byID := func(pv mapi.PropertyValues) map[uint16]any {
		m := make(map[uint16]any, len(pv))
		for _, p := range pv {
			m[p.Tag.ID()] = p.Value
		}
		return m
	}
	w, g := byID(want), byID(got)
	if len(w) != len(g) {
		t.Errorf("%s: property count = %d, want %d", ctx, len(g), len(w))
	}
	for id, wv := range w {
		gv, ok := g[id]
		if !ok {
			t.Errorf("%s: missing property id %#x", ctx, id)
			continue
		}
		if !valuesEqual(wv, gv) {
			t.Errorf("%s: property id %#x = %v, want %v", ctx, id, gv, wv)
		}
	}
}

// assertMessageEqual compares a reconstructed message against the original by
// value across its property bag, recipients, and attachments.
func assertMessageEqual(t *testing.T, ctx string, want, got *oxcmail.Message) {
	t.Helper()
	assertPropsEqual(t, ctx+" props", want.Props, got.Props)
	if len(want.Recipients) != len(got.Recipients) {
		t.Fatalf("%s: recipient count = %d, want %d", ctx, len(got.Recipients), len(want.Recipients))
	}
	for i := range want.Recipients {
		assertPropsEqual(t, fmt.Sprintf("%s recipient %d", ctx, i), want.Recipients[i], got.Recipients[i])
	}
	if len(want.Attachments) != len(got.Attachments) {
		t.Fatalf("%s: attachment count = %d, want %d", ctx, len(got.Attachments), len(want.Attachments))
	}
	for i := range want.Attachments {
		assertPropsEqual(t, fmt.Sprintf("%s attachment %d", ctx, i), want.Attachments[i].Props, got.Attachments[i].Props)
	}
}

// TestImportMessageFullLoop is the headline round-trip: seed a folder, download
// it, drop the messages, then replay the download stream as a client upload and
// assert each message is reconstructed by value (properties, recipients, and
// attachment payload). The replay is same-store so the home-replica source keys
// resolve to the original ids — now absent, so each is an insert. The teeth are
// the by-value equality, which a dropped or swapped value (which a marker-count
// check would miss) fails. Independent oracle remains Outlook-PENDING: producer
// and collector are both hermex code, so a symmetric wire-format error survives.
func TestImportMessageFullLoop(t *testing.T) {
	s := openSeededStore(t)
	fld := int64(mapi.PrivateFIDContacts)
	m1, err := s.CreateMessage(fld, richMsg("first"))
	if err != nil {
		t.Fatal(err)
	}
	m2, err := s.CreateMessage(fld, richMsg("second"))
	if err != nil {
		t.Fatal(err)
	}

	orig := map[uint64]*oxcmail.Message{}
	for _, mid := range []int64{m1, m2} {
		msg, err := s.OpenMessage(mid)
		if err != nil {
			t.Fatal(err)
		}
		orig[uint64(mid)] = msg
	}

	dc, err := s.NewContentDownload(fld, downloadState(t, s), SyncNormal, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	items := drainDownload(t, dc, 64)

	for _, mid := range []int64{m1, m2} {
		if err := s.DeleteObject(mid); err != nil {
			t.Fatal(err)
		}
	}

	mids := uploadDownloadStream(t, s, fld, items, 16)
	if len(mids) != 2 {
		t.Fatalf("reconstructed %d messages, want 2", len(mids))
	}
	for _, mid := range mids {
		want, ok := orig[mid]
		if !ok {
			t.Fatalf("reconstructed unexpected mid %#x", mid)
		}
		got, err := s.OpenMessage(int64(mid))
		if err != nil {
			t.Fatalf("open reconstructed %#x: %v", mid, err)
		}
		assertMessageEqual(t, fmt.Sprintf("mid %#x", mid), want, got)
	}
}

// TestImportMessageByteTearing feeds a body carrying a long string through tiny
// PutBuffer chunks, so the value is torn at many boundaries. The collector's
// parser must reassemble it exactly — surface the Inc 2 parser tests did not
// exercise on the upload path. The symmetric full loop would not catch a tearing
// bug here, so this is asserted directly against the stored value.
func TestImportMessageByteTearing(t *testing.T) {
	s := openSeededStore(t)
	fld := int64(mapi.PrivateFIDContacts)
	mid := uint64(0xB001)
	long := strings.Repeat("torn-body-", 100) // 1000 bytes
	body := encodeStream(t,
		propItem(mapi.PrMessageClass, "IPM.Note"),
		propItem(mapi.PrBody, long),
	)
	um, err := s.ImportMessageChange(fld, 0, importHeader(t, s, mid))
	if err != nil {
		t.Fatal(err)
	}
	col := NewMessageCollector(um)
	for off := 0; off < len(body); off += 7 {
		end := min(off+7, len(body))
		if err := col.PutBuffer(body[off:end]); err != nil {
			t.Fatalf("PutBuffer at %d: %v", off, err)
		}
	}
	if _, err := um.Commit(); err != nil {
		t.Fatal(err)
	}
	got, err := s.OpenMessage(int64(mid))
	if err != nil {
		t.Fatal(err)
	}
	v, ok := got.Props.Get(mapi.PrBody)
	if !ok {
		t.Fatal("body not stored")
	}
	if s, _ := v.(string); s != long {
		t.Errorf("torn body mismatch: got %d bytes, want %d", len(s), len(long))
	}
}

// TestImportMessageUpdatedBranch re-imports an existing message and asserts the
// store bumps its change number, replaces (not appends) its content, and that a
// download holding the prior version is told the message both changed and is an
// update it already had — the delta engine's "updated" branch, which only this
// re-import write path exercises with real data.
func TestImportMessageUpdatedBranch(t *testing.T) {
	s := openSeededStore(t)
	fld := int64(mapi.PrivateFIDContacts)
	mid := uint64(0xC001)

	importOne(t, s, fld, mid, 0, encodeStream(t,
		propItem(mapi.PrMessageClass, "IPM.Note"), propItem(mapi.PrSubject, "v1")))
	c1 := msgCN(t, s, int64(mid))

	importOne(t, s, fld, mid, 0, encodeStream(t,
		propItem(mapi.PrMessageClass, "IPM.Note"), propItem(mapi.PrSubject, "v2")))
	c2 := msgCN(t, s, int64(mid))
	if c2 <= c1 {
		t.Fatalf("re-import did not bump the change number (c1=%d, c2=%d)", c1, c2)
	}

	seen := ics.NewIDSet(ics.FormIDLoose, nil)
	seen.AppendRange(homeReplID, 1, c1)
	res, err := s.GetContentSync(ContentSyncRequest{FolderID: fld, Given: looseSet(mid), Seen: seen})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(res.ChangedMIDs, mid) {
		t.Errorf("re-imported message %#x not reported changed", mid)
	}
	if !slices.Contains(res.UpdatedMIDs, mid) {
		t.Errorf("re-imported message %#x not reported as an update", mid)
	}

	got, err := s.OpenMessage(int64(mid))
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := got.Props.Get(mapi.PrSubject); v != "v2" {
		t.Errorf("subject = %v, want v2 (re-import must replace, not append)", v)
	}
}

// TestImportNamedPropertyRemap checks that a named property carried inline is
// allocated a store-local id on import and resolves back to the same name — the
// remap that lets a client's id space and the store's differ.
func TestImportNamedPropertyRemap(t *testing.T) {
	s := openSeededStore(t)
	fld := int64(mapi.PrivateFIDContacts)
	mid := uint64(0xD001)
	guid, err := mapi.ParseGUID("00020329-0000-0000-c000-000000000046")
	if err != nil {
		t.Fatal(err)
	}
	name := mapi.PropertyName{Kind: mapi.MnidID, GUID: guid, LID: 0x8201}

	importOne(t, s, fld, mid, 0, encodeStream(t,
		propItem(mapi.PrMessageClass, "IPM.Note"),
		namedItem(0x8001, mapi.PtLong, &name, int32(7))))

	got, err := s.OpenMessage(int64(mid))
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, p := range got.Props {
		if p.Tag.ID() < 0x8000 {
			continue
		}
		found = true
		if v, _ := p.Value.(int32); v != 7 {
			t.Errorf("named property value = %v, want 7", p.Value)
		}
		rn, ok, err := s.NamedPropName(p.Tag.ID())
		if err != nil {
			t.Fatal(err)
		}
		if !ok || rn != name {
			t.Errorf("named id %#x resolves to %+v (ok=%v), want %+v", p.Tag.ID(), rn, ok, name)
		}
	}
	if !found {
		t.Error("named property was not stored under a store-local id")
	}
}

// TestImportRejectsEmbedded locks the conscious v1 fork: an embedded-message
// marker fails loud rather than silently dropping the embedded content.
func TestImportRejectsEmbedded(t *testing.T) {
	s := openSeededStore(t)
	um, err := s.ImportMessageChange(int64(mapi.PrivateFIDContacts), 0, importHeader(t, s, 0xE001))
	if err != nil {
		t.Fatal(err)
	}
	col := NewMessageCollector(um)
	body := encodeStream(t,
		ics.Item{IsMarker: true, Marker: ics.MarkerNewAttach},
		ics.Item{IsMarker: true, Marker: ics.MarkerStartEmbed},
	)
	if err := col.PutBuffer(body); err == nil {
		t.Fatal("expected embedded-message upload to be rejected")
	}
}

// TestImportRejectsStateMetaTag checks that an ICS state idset meta-tag in the
// content stream is rejected — state travels via the upload-state-stream ROPs, and
// silently storing it as a message property would corrupt the message.
func TestImportRejectsStateMetaTag(t *testing.T) {
	s := openSeededStore(t)
	um, err := s.ImportMessageChange(int64(mapi.PrivateFIDContacts), 0, importHeader(t, s, 0xE002))
	if err != nil {
		t.Fatal(err)
	}
	col := NewMessageCollector(um)
	body := encodeStream(t, ics.Item{Prop: &ics.StreamProp{
		Tag:   mapi.PropTag(0x67960102), // MetaTagCnsetSeen
		Value: []byte{0x01, 0x02},
	}})
	if err := col.PutBuffer(body); err == nil {
		t.Fatal("expected an ICS state meta-tag in the content stream to be rejected")
	}
}

// TestImportDeletes deletes one of two messages by its home-replica source key and
// asserts the named message is gone, the other survives, and a repeat delete of an
// absent id is an idempotent no-op.
func TestImportDeletes(t *testing.T) {
	s := openSeededStore(t)
	fld := int64(mapi.PrivateFIDContacts)
	m1, err := s.CreateMessage(fld, richMsg("keep"))
	if err != nil {
		t.Fatal(err)
	}
	m2, err := s.CreateMessage(fld, richMsg("drop"))
	if err != nil {
		t.Fatal(err)
	}
	home, err := s.replicaGUID()
	if err != nil {
		t.Fatal(err)
	}

	deleted, err := s.ImportDeletes(fld, [][]byte{sourceKey(home, uint64(m2))})
	if err != nil {
		t.Fatal(err)
	}
	if len(deleted) != 1 || deleted[0] != uint64(m2) {
		t.Fatalf("deleted = %v, want [%d]", deleted, m2)
	}
	if _, err := s.OpenMessage(m2); !errors.Is(err, ErrNotFound) {
		t.Errorf("deleted message still present (err=%v)", err)
	}
	if _, err := s.OpenMessage(m1); err != nil {
		t.Errorf("kept message vanished: %v", err)
	}

	again, err := s.ImportDeletes(fld, [][]byte{sourceKey(home, uint64(m2))})
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != 0 {
		t.Errorf("re-delete of an absent id reported %v, want none", again)
	}
}

// TestImportReadStateChanges marks a message read by its source key and asserts the
// stored flag flipped, a read change number was recorded, and a download holding
// the body but not that read change is told the read state changed — the engine's
// read-state branch, which only this write path feeds with a real read change
// number. A repeat of the same flag is a no-op.
func TestImportReadStateChanges(t *testing.T) {
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
	cn := msgCN(t, s, mid)

	readCNs, err := s.ImportReadStateChanges(fld, []ReadStateChange{
		{SourceKey: sourceKey(home, uint64(mid)), MarkRead: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(readCNs) != 1 {
		t.Fatalf("read change numbers = %v, want one", readCNs)
	}
	var read int
	var rcn sql.NullInt64
	if err := s.objdb.QueryRow(`SELECT read_state, read_cn FROM messages WHERE message_id=?`, mid).Scan(&read, &rcn); err != nil {
		t.Fatal(err)
	}
	if read != 1 {
		t.Errorf("read_state = %d, want 1", read)
	}
	if !rcn.Valid || uint64(rcn.Int64) != readCNs[0] {
		t.Errorf("stored read_cn = %v, want %d", rcn, readCNs[0])
	}

	seen := ics.NewIDSet(ics.FormIDLoose, nil)
	seen.AppendRange(homeReplID, 1, cn)
	res, err := s.GetContentSync(ContentSyncRequest{
		FolderID: fld,
		Given:    looseSet(uint64(mid)),
		Seen:     seen,
		Read:     ics.NewIDSet(ics.FormIDLoose, nil), // read class enabled, the read change unseen
	})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(res.ReadMIDs, uint64(mid)) {
		t.Errorf("read-state change for %#x not reported (read=%v unread=%v)", mid, res.ReadMIDs, res.UnreadMIDs)
	}

	again, err := s.ImportReadStateChanges(fld, []ReadStateChange{
		{SourceKey: sourceKey(home, uint64(mid)), MarkRead: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != 0 {
		t.Errorf("re-applying the same read flag reported %v, want none", again)
	}
}

// hierHeader builds the fixed-order hierarchy-change identity set for a home folder
// id. A nil parent source key (sent as empty) parents the folder under the
// collector root.
func hierHeader(t *testing.T, s *Store, fid uint64, parentSK []byte, dispName string) mapi.PropertyValues {
	t.Helper()
	home, err := s.replicaGUID()
	if err != nil {
		t.Fatal(err)
	}
	ck, err := changeKey(home, 1)
	if err != nil {
		t.Fatal(err)
	}
	pcl, err := predecessorChangeList(home, 1)
	if err != nil {
		t.Fatal(err)
	}
	if parentSK == nil {
		parentSK = []byte{}
	}
	return mapi.PropertyValues{
		{Tag: mapi.PrParentSourceKey, Value: parentSK},
		{Tag: mapi.PrSourceKey, Value: sourceKey(home, fid)},
		{Tag: mapi.PrLastModificationTime, Value: mapi.UnixToNTTime(time.Now())},
		{Tag: mapi.PrChangeKey, Value: ck},
		{Tag: mapi.PrPredecessorChangeList, Value: pcl},
		{Tag: mapi.PrDisplayName, Value: dispName},
	}
}

// TestImportHierarchyCreate imports a new folder by its home source key and asserts
// it lands at that id under the collector root with the uploaded display name and a
// store-derived change key.
func TestImportHierarchyCreate(t *testing.T) {
	s := openSeededStore(t)
	root := int64(mapi.PrivateFIDIPMSubtree)
	fid := uint64(0x200001)
	got, err := s.ImportHierarchyChange(root, hierHeader(t, s, fid, nil, "Imported"),
		mapi.PropertyValues{{Tag: mapi.PrContainerClass, Value: mapi.ContainerClassNote}})
	if err != nil {
		t.Fatal(err)
	}
	if got != fid {
		t.Fatalf("folder id = %#x, want %#x", got, fid)
	}
	exists, err := s.FolderExists(int64(fid))
	if err != nil {
		t.Fatal(err)
	}
	if !exists {
		t.Fatal("imported folder does not exist")
	}
	props, err := s.GetFolderProperties(int64(fid))
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := props.Get(mapi.PrDisplayName); v != "Imported" {
		t.Errorf("display name = %v, want Imported", v)
	}
	if _, ok := props.Get(mapi.PrChangeKey); !ok {
		t.Error("imported folder missing the store-derived PR_CHANGE_KEY")
	}
	var parent int64
	if err := s.objdb.QueryRow(`SELECT parent_id FROM folders WHERE folder_id=?`, int64(fid)).Scan(&parent); err != nil {
		t.Fatal(err)
	}
	if parent != root {
		t.Errorf("parent = %d, want %d (the collector root)", parent, root)
	}
}

// TestImportHierarchyUpdate re-imports an existing folder with a new name and
// asserts it is updated in place — name changed, change number bumped, not
// duplicated.
func TestImportHierarchyUpdate(t *testing.T) {
	s := openSeededStore(t)
	root := int64(mapi.PrivateFIDIPMSubtree)
	fid := uint64(0x200002)
	if _, err := s.ImportHierarchyChange(root, hierHeader(t, s, fid, nil, "v1"), nil); err != nil {
		t.Fatal(err)
	}
	c1 := folderCN(t, s, int64(fid))
	if _, err := s.ImportHierarchyChange(root, hierHeader(t, s, fid, nil, "v2"), nil); err != nil {
		t.Fatal(err)
	}
	c2 := folderCN(t, s, int64(fid))
	if c2 <= c1 {
		t.Fatalf("re-import did not bump the folder change number (c1=%d, c2=%d)", c1, c2)
	}
	props, err := s.GetFolderProperties(int64(fid))
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := props.Get(mapi.PrDisplayName); v != "v2" {
		t.Errorf("display name = %v, want v2", v)
	}
	var count int
	if err := s.objdb.QueryRow(`SELECT COUNT(*) FROM folders WHERE folder_id=?`, int64(fid)).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("folder rows at %#x = %d, want 1 (re-import must update, not duplicate)", fid, count)
	}
}

// TestImportHierarchyParentResolution imports a child folder whose parent source
// key names a previously imported folder and asserts the child is parented under
// it.
func TestImportHierarchyParentResolution(t *testing.T) {
	s := openSeededStore(t)
	root := int64(mapi.PrivateFIDIPMSubtree)
	home, err := s.replicaGUID()
	if err != nil {
		t.Fatal(err)
	}
	parentFID := uint64(0x200003)
	if _, err := s.ImportHierarchyChange(root, hierHeader(t, s, parentFID, nil, "Parent"), nil); err != nil {
		t.Fatal(err)
	}
	childFID := uint64(0x200004)
	if _, err := s.ImportHierarchyChange(root, hierHeader(t, s, childFID, sourceKey(home, parentFID), "Child"), nil); err != nil {
		t.Fatal(err)
	}
	var parent int64
	if err := s.objdb.QueryRow(`SELECT parent_id FROM folders WHERE folder_id=?`, int64(childFID)).Scan(&parent); err != nil {
		t.Fatal(err)
	}
	if uint64(parent) != parentFID {
		t.Errorf("child parent = %d, want %#x", parent, parentFID)
	}
}
