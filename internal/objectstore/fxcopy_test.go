package objectstore

import (
	"testing"

	"hermex/internal/ics"
	"hermex/internal/mapi"
)

// parseCopyItems reassembles a generic-copy stream into its flat element list.
func parseCopyItems(t *testing.T, body []byte) []ics.Item {
	t.Helper()
	var ps ics.Parser
	ps.Feed(body)
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

// drainCopy reads an entire generic-copy source into one buffer.
func drainCopy(t *testing.T, c *CopyContext) []byte {
	t.Helper()
	var out []byte
	for {
		chunk, last, err := c.GetBuffer(1 << 16)
		if err != nil {
			t.Fatalf("GetBuffer: %v", err)
		}
		out = append(out, chunk...)
		if last {
			return out
		}
	}
}

// reconstructMessage replays a generic-copy messageContent body the way a client
// uploads it: a fresh import-change call supplies the identity header, the body
// (property list + recipient and attachment lists, no INCRSYNCCHG framing) is fed
// to the message collector, and the message is committed. It returns the new id.
func reconstructMessage(t *testing.T, dst *Store, folderID int64, body []byte) uint64 {
	t.Helper()
	um, err := dst.ImportMessageChange(folderID, 0, importHeader(t, dst, 0x1000))
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

// TestCopyToMessageRoundTrip renders a stored message as a generic-copy
// messageContent (CopyTo, no exclusions), replays the stream through the message
// collector, and asserts the message is reconstructed by value — property bag,
// recipients, and attachment payload. It proves the messageContent grammar (a bare
// property list plus the recipient/attachment sub-object lists, with no
// INCRSYNCCHG/INCRSYNCMESSAGE framing) parses back through hermex's own upload path.
// Independent oracle remains Outlook-PENDING: producer and collector are both hermex
// code, so this is a self-consistency check, not a wire-format proof; the framing is
// pinned to the [MS-OXCFXICS] 2.2.4 grammar.
func TestCopyToMessageRoundTrip(t *testing.T) {
	s := openSeededStore(t)
	fld := int64(mapi.PrivateFIDContacts)
	mid, err := s.CreateMessage(fld, richMsg("copyto round-trip"))
	if err != nil {
		t.Fatal(err)
	}
	want, err := s.OpenMessage(mid)
	if err != nil {
		t.Fatal(err)
	}

	src, err := s.NewCopyToMessageSource(mid, nil)
	if err != nil {
		t.Fatalf("NewCopyToMessageSource: %v", err)
	}
	body := drainCopy(t, src)

	dst := openSeededStore(t)
	id := reconstructMessage(t, dst, int64(mapi.PrivateFIDInbox), body)
	got, err := dst.OpenMessage(int64(id))
	if err != nil {
		t.Fatalf("open reconstructed: %v", err)
	}
	assertMessageEqual(t, "CopyTo", want, got)
}

// TestCopyToMessageExclusion checks that an excluded property tag is dropped from
// the rendered messageContent while the rest of the bag still round-trips.
func TestCopyToMessageExclusion(t *testing.T) {
	s := openSeededStore(t)
	fld := int64(mapi.PrivateFIDContacts)
	mid, err := s.CreateMessage(fld, richMsg("exclusion"))
	if err != nil {
		t.Fatal(err)
	}

	src, err := s.NewCopyToMessageSource(mid, []mapi.PropTag{mapi.PrImportance})
	if err != nil {
		t.Fatalf("NewCopyToMessageSource: %v", err)
	}
	body := drainCopy(t, src)

	dst := openSeededStore(t)
	id := reconstructMessage(t, dst, int64(mapi.PrivateFIDInbox), body)
	got, err := dst.OpenMessage(int64(id))
	if err != nil {
		t.Fatalf("open reconstructed: %v", err)
	}
	if _, ok := got.Props.Get(mapi.PrImportance); ok {
		t.Errorf("excluded property PrImportance survived the copy")
	}
	if v, ok := got.Props.Get(mapi.PrSubject); !ok || v != "exclusion" {
		t.Errorf("PrSubject = %v (ok=%v), want \"exclusion\"", v, ok)
	}
}

// TestCopyPropertiesMessageRoundTrip renders a stored message's listed properties as
// a generic-copy propList (no recipients or attachments) and asserts only the listed
// properties survive the round trip and no sub-objects are carried.
func TestCopyPropertiesMessageRoundTrip(t *testing.T) {
	s := openSeededStore(t)
	fld := int64(mapi.PrivateFIDContacts)
	mid, err := s.CreateMessage(fld, richMsg("copyprops"))
	if err != nil {
		t.Fatal(err)
	}

	src, err := s.NewCopyPropertiesMessageSource(mid, []mapi.PropTag{mapi.PrSubject, mapi.PrMessageClass})
	if err != nil {
		t.Fatalf("NewCopyPropertiesMessageSource: %v", err)
	}
	body := drainCopy(t, src)

	dst := openSeededStore(t)
	id := reconstructMessage(t, dst, int64(mapi.PrivateFIDInbox), body)
	got, err := dst.OpenMessage(int64(id))
	if err != nil {
		t.Fatalf("open reconstructed: %v", err)
	}
	if v, ok := got.Props.Get(mapi.PrSubject); !ok || v != "copyprops" {
		t.Errorf("PrSubject = %v (ok=%v), want \"copyprops\"", v, ok)
	}
	if _, ok := got.Props.Get(mapi.PrBody); ok {
		t.Errorf("PrBody survived a CopyProperties that did not list it")
	}
	if len(got.Recipients) != 0 {
		t.Errorf("CopyProperties carried %d recipients, want 0 (propList has no sub-objects)", len(got.Recipients))
	}
	if len(got.Attachments) != 0 {
		t.Errorf("CopyProperties carried %d attachments, want 0", len(got.Attachments))
	}
}

// TestCopyMessagesMessageList renders two messages of a folder (one normal, one
// associated) as a generic-copy messageList and asserts the per-message framing: a
// StartMessage for the normal message, a StartFAIMsg for the associated one, and a
// matching EndMessage for each. The contained messageContent bytes come from the
// same writeCopyMessageContent that TestCopyToMessageRoundTrip reconstructs by
// value, so this test covers the NEW framing only; an independent oracle remains
// Outlook-PENDING (producer and parser are both hermex code).
func TestCopyMessagesMessageList(t *testing.T) {
	s := openSeededStore(t)
	fld := int64(mapi.PrivateFIDContacts)
	mid1, err := s.CreateMessage(fld, richMsg("normal"))
	if err != nil {
		t.Fatal(err)
	}
	fai := richMsg("fai")
	fai.Props = append(fai.Props, mapi.TaggedPropVal{Tag: mapi.PrAssociated, Value: true})
	mid2, err := s.CreateMessage(fld, fai)
	if err != nil {
		t.Fatal(err)
	}

	src, err := s.NewCopyMessagesSource(fld, []int64{mid1, mid2}, nil)
	if err != nil {
		t.Fatalf("NewCopyMessagesSource: %v", err)
	}
	items := parseCopyItems(t, drainCopy(t, src))

	if n := countMarkers(items, ics.MarkerStartMessage); n != 1 {
		t.Errorf("StartMessage count = %d, want 1 (the normal message)", n)
	}
	if n := countMarkers(items, ics.MarkerStartFAIMsg); n != 1 {
		t.Errorf("StartFAIMsg count = %d, want 1 (the associated message)", n)
	}
	if n := countMarkers(items, ics.MarkerEndMessage); n != 2 {
		t.Errorf("EndMessage count = %d, want 2 (one per message)", n)
	}
}

// TestCopyMessagesSingleRoundTrip frames one message as a messageList, strips the
// leading StartMessage and trailing EndMessage markers (4 bytes each), and replays
// the bare messageContent through the upload path — proving the framing wraps a
// clean, reconstructable messageContent at the segment boundary.
func TestCopyMessagesSingleRoundTrip(t *testing.T) {
	s := openSeededStore(t)
	fld := int64(mapi.PrivateFIDContacts)
	mid, err := s.CreateMessage(fld, richMsg("messagelist round-trip"))
	if err != nil {
		t.Fatal(err)
	}
	want, err := s.OpenMessage(mid)
	if err != nil {
		t.Fatal(err)
	}

	src, err := s.NewCopyMessagesSource(fld, []int64{mid}, nil)
	if err != nil {
		t.Fatalf("NewCopyMessagesSource: %v", err)
	}
	body := drainCopy(t, src)
	if len(body) < 8 {
		t.Fatalf("messageList body = %d bytes, too short to carry Start/End markers", len(body))
	}
	content := body[4 : len(body)-4] // strip StartMessage (4) + EndMessage (4)

	dst := openSeededStore(t)
	id := reconstructMessage(t, dst, int64(mapi.PrivateFIDInbox), content)
	got, err := dst.OpenMessage(int64(id))
	if err != nil {
		t.Fatalf("open reconstructed: %v", err)
	}
	assertMessageEqual(t, "CopyMessages", want, got)
}

// TestCopyMessagesRejectsForeignMessage asserts a message id that is not a live row
// of the source folder is refused (ErrNotFound) rather than emitted — a CopyMessages
// source streams only messages of its own folder.
func TestCopyMessagesRejectsForeignMessage(t *testing.T) {
	s := openSeededStore(t)
	mid, err := s.CreateMessage(int64(mapi.PrivateFIDContacts), richMsg("foreign"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.NewCopyMessagesSource(int64(mapi.PrivateFIDInbox), []int64{mid}, nil); err == nil {
		t.Errorf("NewCopyMessagesSource accepted a message outside the source folder")
	}
}

// firstMessageBody returns the items strictly between the first StartMessage (or
// StartFAIMsg) and its matching EndMessage — the bare messageContent of the first
// message in a folderContent/messageList stream.
func firstMessageBody(t *testing.T, items []ics.Item) []ics.Item {
	t.Helper()
	start := -1
	for i, it := range items {
		if it.IsMarker && (it.Marker == ics.MarkerStartMessage || it.Marker == ics.MarkerStartFAIMsg) {
			start = i
			break
		}
	}
	if start < 0 {
		t.Fatal("no StartMessage/StartFAIMsg marker found")
	}
	for i := start + 1; i < len(items); i++ {
		if items[i].IsMarker && items[i].Marker == ics.MarkerEndMessage {
			return items[start+1 : i]
		}
	}
	t.Fatal("no EndMessage marker after the first StartMessage")
	return nil
}

// reserializeMessageContent re-encodes a parsed messageContent (markers + props)
// back to wire bytes, the inverse of parseCopyItems for one message body. It lets a
// folderContent test feed a contained messageContent through the upload path.
func reserializeMessageContent(t *testing.T, items []ics.Item) []byte {
	t.Helper()
	pr := &ics.Producer{}
	for _, it := range items {
		if it.IsMarker {
			pr.WriteMarker(it.Marker)
			continue
		}
		if err := pr.WriteProp(*it.Prop); err != nil {
			t.Fatalf("WriteProp: %v", err)
		}
	}
	var out []byte
	for {
		chunk, done := pr.ReadBuffer(1 << 16)
		out = append(out, chunk...)
		if done {
			return out
		}
	}
}

// TestCopyFolderTopFolder renders a folder (one message) with a subfolder (one
// message) as a generic-copy topFolder and asserts the no-del-props grammar: one
// STARTTOPFLD wrapping the folder content, one STARTSUBFLD for the subfolder, an
// ENDFOLDER closing each, and a framed message in each folder. It then reconstructs
// the first contained messageContent through the upload path. The folder framing is
// pinned to the [MS-OXCFXICS] make_topfolder grammar; an independent oracle remains
// Outlook-PENDING (producer and parser are both hermex code).
func TestCopyFolderTopFolder(t *testing.T) {
	s := openSeededStore(t)
	parent, err := s.CreateFolder(nil, "CopyFolderParent")
	if err != nil {
		t.Fatal(err)
	}
	pmid, err := s.CreateMessage(parent, richMsg("parent message"))
	if err != nil {
		t.Fatal(err)
	}
	sub, err := s.CreateFolder(&parent, "CopyFolderSub")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateMessage(sub, richMsg("sub message")); err != nil {
		t.Fatal(err)
	}

	src, err := s.NewCopyFolderSource(parent, true)
	if err != nil {
		t.Fatalf("NewCopyFolderSource: %v", err)
	}
	items := parseCopyItems(t, drainCopy(t, src))

	if n := countMarkers(items, ics.MarkerStartTopFld); n != 1 {
		t.Errorf("StartTopFld count = %d, want 1", n)
	}
	if n := countMarkers(items, ics.MarkerStartSubFld); n != 1 {
		t.Errorf("StartSubFld count = %d, want 1 (one subfolder)", n)
	}
	if n := countMarkers(items, ics.MarkerEndFolder); n != 2 {
		t.Errorf("EndFolder count = %d, want 2 (topfolder + subfolder)", n)
	}
	if n := countMarkers(items, ics.MarkerStartMessage); n != 2 {
		t.Errorf("StartMessage count = %d, want 2 (one per folder)", n)
	}
	if n := countMarkers(items, ics.MarkerEndMessage); n != 2 {
		t.Errorf("EndMessage count = %d, want 2", n)
	}

	want, err := s.OpenMessage(pmid)
	if err != nil {
		t.Fatal(err)
	}
	content := reserializeMessageContent(t, firstMessageBody(t, items))
	dst := openSeededStore(t)
	id := reconstructMessage(t, dst, int64(mapi.PrivateFIDInbox), content)
	got, err := dst.OpenMessage(int64(id))
	if err != nil {
		t.Fatalf("open reconstructed: %v", err)
	}
	assertMessageEqual(t, "CopyFolder", want, got)
}

// TestCopyFolderNoSubfolders checks the subfolders=false path (CopyFolder without
// the Move/CopySubfolders flag) omits the descendant folders entirely.
func TestCopyFolderNoSubfolders(t *testing.T) {
	s := openSeededStore(t)
	parent, err := s.CreateFolder(nil, "FlatParent")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateMessage(parent, richMsg("flat message")); err != nil {
		t.Fatal(err)
	}
	sub, err := s.CreateFolder(&parent, "Ignored")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateMessage(sub, richMsg("ignored message")); err != nil {
		t.Fatal(err)
	}

	src, err := s.NewCopyFolderSource(parent, false)
	if err != nil {
		t.Fatalf("NewCopyFolderSource: %v", err)
	}
	items := parseCopyItems(t, drainCopy(t, src))
	if n := countMarkers(items, ics.MarkerStartSubFld); n != 0 {
		t.Errorf("StartSubFld count = %d, want 0 (subfolders excluded)", n)
	}
	if n := countMarkers(items, ics.MarkerStartMessage); n != 1 {
		t.Errorf("StartMessage count = %d, want 1 (only the parent's message)", n)
	}
	if n := countMarkers(items, ics.MarkerEndFolder); n != 1 {
		t.Errorf("EndFolder count = %d, want 1 (topfolder only)", n)
	}
}

// TestCopyPropertiesEmptyIncludeCopiesNothing checks the empty inclusive set selects
// no properties rather than falling through to the keep-all (exclusion) default.
func TestCopyPropertiesEmptyIncludeCopiesNothing(t *testing.T) {
	s := openSeededStore(t)
	fld := int64(mapi.PrivateFIDContacts)
	mid, err := s.CreateMessage(fld, richMsg("empty"))
	if err != nil {
		t.Fatal(err)
	}
	src, err := s.NewCopyPropertiesMessageSource(mid, nil)
	if err != nil {
		t.Fatalf("NewCopyPropertiesMessageSource: %v", err)
	}
	if body := drainCopy(t, src); len(body) != 0 {
		t.Errorf("empty CopyProperties produced %d bytes, want 0", len(body))
	}
}
