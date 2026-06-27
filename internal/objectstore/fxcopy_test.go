package objectstore

import (
	"testing"

	"hermex/internal/mapi"
)

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
