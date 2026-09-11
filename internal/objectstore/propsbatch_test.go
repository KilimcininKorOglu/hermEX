package objectstore

import (
	"testing"

	"hermex/internal/mapi"
)

// batchTags is the property set these tests read: one the seeded messages carry
// and one they do not, so an absent property is covered too.
var batchTags = []mapi.PropTag{mapi.PrSubject, mapi.PrInternetMessageID}

// wantSameBags fails the test unless the batched read returned, for every id, the
// same values the per-message read returns.
func wantSameBags(t *testing.T, st *Store, ids []int64, got map[int64]mapi.PropertyValues) {
	t.Helper()
	for _, id := range ids {
		want, err := st.GetMessageProperties(id, batchTags...)
		if err != nil {
			t.Fatalf("GetMessageProperties(%d): %v", id, err)
		}
		for _, tag := range batchTags {
			w, wok := want.Get(tag)
			g, gok := got[id].Get(tag)
			if wok != gok {
				t.Errorf("message %d tag %s: batched present=%v, per-message present=%v", id, tag, gok, wok)
				continue
			}
			if wok && g != w {
				t.Errorf("message %d tag %s: batched %v, per-message %v", id, tag, g, w)
			}
		}
	}
}

// TestMessagePropertiesBatchMatchesPerMessageRead proves the batched reader
// returns what the per-message reader returns, which is what lets a caller that
// reads the same tags for a whole folder use it.
func TestMessagePropertiesBatchMatchesPerMessageRead(t *testing.T) {
	st := seedListIDs(t, 5)
	ids, err := st.ListMessageIDs(int64(mapi.PrivateFIDInbox))
	if err != nil {
		t.Fatal(err)
	}

	got, err := st.MessagePropertiesBatch(ids, batchTags)
	if err != nil {
		t.Fatal(err)
	}
	wantSameBags(t, st, ids, got)
}

// TestMessagePropertiesBatchSpansChunks proves no id is dropped when the ids do
// not fit in one query. The chunk shrinks as the tag list grows, so a tag list
// that fills the parameter budget forces one id per query and makes this read
// span twelve of them.
func TestMessagePropertiesBatchSpansChunks(t *testing.T) {
	st := seedListIDs(t, 12)
	ids, err := st.ListMessageIDs(int64(mapi.PrivateFIDInbox))
	if err != nil {
		t.Fatal(err)
	}
	// Fill the budget with named-property-range tags the seeded messages do not
	// carry, so only the batchTags values can come back.
	tags := append([]mapi.PropTag(nil), batchTags...)
	for id := uint16(0x8000); len(tags) < maxBatchParams; id++ {
		tags = append(tags, mapi.MakeTag(id, mapi.PtUnicode))
	}
	if n := batchIDChunk(len(tags)); n != 1 {
		t.Fatalf("chunk with a full tag list = %d, want 1", n)
	}

	got, err := st.MessagePropertiesBatch(ids, tags)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(ids) {
		t.Errorf("batched read covered %d of %d messages", len(got), len(ids))
	}
	wantSameBags(t, st, ids, got)
}

// TestMessagePropertiesBatchEdgeCases proves an unknown id is absent from the
// result rather than an error, and that an empty tag list yields an empty result
// rather than an error. It does NOT prove the empty-tag guard is load-bearing:
// SQLite answers `proptag IN ()` with no rows, so removing the guard leaves this
// test green. The guard saves a round trip, and that is all it does.
func TestMessagePropertiesBatchEdgeCases(t *testing.T) {
	st := seedListIDs(t, 2)
	ids, err := st.ListMessageIDs(int64(mapi.PrivateFIDInbox))
	if err != nil {
		t.Fatal(err)
	}

	withUnknown := append(append([]int64(nil), ids...), 999999)
	got, err := st.MessagePropertiesBatch(withUnknown, batchTags)
	if err != nil {
		t.Fatalf("an unknown id must not fail the read: %v", err)
	}
	if _, ok := got[999999]; ok {
		t.Error("an unknown id is present in the result")
	}

	none, err := st.MessagePropertiesBatch(ids, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(none) != 0 {
		t.Errorf("an empty tag list returned %d bags, want 0", len(none))
	}
}
