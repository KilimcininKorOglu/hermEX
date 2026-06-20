package nspi

import (
	"encoding/binary"
	"slices"
	"strings"
	"testing"

	"hermex/internal/mapi"
)

// SetDelegates lets delegatingGAL satisfy delegateWriter for the ModLinkAtt tests;
// it mutates the shared map so a test can read back the persisted list.
func (d delegatingGAL) SetDelegates(userAddr string, list []string) error {
	d.delegates[strings.ToLower(userAddr)] = list
	return nil
}

// ephemeralEID builds an EphemeralEntryID carrying a MId at offset 28.
func ephemeralEID(mid uint32) []byte {
	b := make([]byte, 32)
	b[0] = entryidTypeEphemeral
	binary.LittleEndian.PutUint32(b[28:], mid)
	return b
}

func delegateWriterServer() (*Server, delegatingGAL) {
	w := delegatingGAL{
		maskedGAL: maskedGAL{
			{DisplayName: "boss@hermex.test", Address: "boss@hermex.test", DisplayType: rtUser},
			{DisplayName: "alice@hermex.test", Address: "alice@hermex.test", DisplayType: rtUser},
			{DisplayName: "carol@hermex.test", Address: "carol@hermex.test", DisplayType: rtUser},
		},
		delegates: map[string][]string{},
	}
	return NewServer(w, testGUID), w
}

// TestModLinkAttAddsBothEntryIDFormsThenRemoves proves a caller adds delegates by
// both a permanent (DN-bearing) and an ephemeral (MId-bearing) entry id, the list
// dedupes on re-add, and MOD_FLAG_DELETE removes a delegate. This is the
// self-service write half of delegation; without it Outlook's "Add delegate"
// cannot persist.
func TestModLinkAttAddsBothEntryIDFormsThenRemoves(t *testing.T) {
	s, w := delegateWriterServer()
	g := s.snapshot()
	boss, _ := g.userByAddress("boss@hermex.test")
	carol, _ := g.userByAddress("carol@hermex.test")

	// Add alice (permanent EID) and carol (ephemeral EID).
	add := modLinkAttRequest{
		proptag:  uint32(mapi.PrEmsAbPublicDelegates),
		mid:      boss.mid,
		entryIDs: [][]byte{permanentEntryID(dtMailuser, userDN("alice@hermex.test")), ephemeralEID(carol.mid)},
	}
	if code := s.modLinkAttCore(add, "boss@hermex.test"); code != ecSuccess {
		t.Fatalf("add result = %#x, want ecSuccess", code)
	}
	got := slices.Clone(w.delegates["boss@hermex.test"])
	slices.Sort(got)
	if want := []string{"alice@hermex.test", "carol@hermex.test"}; !slices.Equal(got, want) {
		t.Fatalf("after add, delegates = %v, want %v", got, want)
	}

	// Re-adding alice does not duplicate her (set semantics).
	if code := s.modLinkAttCore(add, "boss@hermex.test"); code != ecSuccess {
		t.Fatalf("re-add result = %#x", code)
	}
	if n := len(w.delegates["boss@hermex.test"]); n != 2 {
		t.Errorf("after re-add, %d delegates, want 2 (no duplicate)", n)
	}

	// Remove alice (MOD_FLAG_DELETE) by her permanent EID.
	del := modLinkAttRequest{
		flags:    modFlagDelete,
		proptag:  uint32(mapi.PrEmsAbPublicDelegates),
		mid:      boss.mid,
		entryIDs: [][]byte{permanentEntryID(dtMailuser, userDN("alice@hermex.test"))},
	}
	if code := s.modLinkAttCore(del, "boss@hermex.test"); code != ecSuccess {
		t.Fatalf("delete result = %#x", code)
	}
	if got := w.delegates["boss@hermex.test"]; !slices.Equal(got, []string{"carol@hermex.test"}) {
		t.Errorf("after delete, delegates = %v, want [carol]", got)
	}
}

// TestModLinkAttDeniesEditingAnotherUsersList is the security assertion: a caller
// may edit only their own delegate list. Editing boss's list while authenticated
// as someone else must be ecAccessDenied and must not mutate the list.
func TestModLinkAttDeniesEditingAnotherUsersList(t *testing.T) {
	s, w := delegateWriterServer()
	g := s.snapshot()
	boss, _ := g.userByAddress("boss@hermex.test")
	req := modLinkAttRequest{
		proptag:  uint32(mapi.PrEmsAbPublicDelegates),
		mid:      boss.mid,
		entryIDs: [][]byte{permanentEntryID(dtMailuser, userDN("alice@hermex.test"))},
	}
	if code := s.modLinkAttCore(req, "alice@hermex.test"); code != ecAccessDenied {
		t.Fatalf("editing another user's list = %#x, want ecAccessDenied", code)
	}
	if len(w.delegates["boss@hermex.test"]) != 0 {
		t.Error("a denied ModLinkAtt still mutated the delegate list")
	}
}

// TestModLinkAttRejectsBadRequests covers the guard rungs: a non-delegates proptag
// is unsupported, a zero mid is an invalid object, and a malformed/short entry id
// is skipped without a panic (the op still succeeds, adding nothing). The 25-byte
// id sits in the dangerous 20..28 range that a missing bound check would slice past.
func TestModLinkAttRejectsBadRequests(t *testing.T) {
	s, w := delegateWriterServer()
	g := s.snapshot()
	boss, _ := g.userByAddress("boss@hermex.test")

	if code := s.modLinkAttCore(modLinkAttRequest{proptag: uint32(mapi.PrDisplayName), mid: boss.mid}, "boss@hermex.test"); code != ecNotSupported {
		t.Errorf("wrong proptag = %#x, want ecNotSupported", code)
	}
	if code := s.modLinkAttCore(modLinkAttRequest{proptag: uint32(mapi.PrEmsAbPublicDelegates), mid: 0}, "boss@hermex.test"); code != ecInvalidObject {
		t.Errorf("mid==0 = %#x, want ecInvalidObject", code)
	}
	garbage := modLinkAttRequest{
		proptag:  uint32(mapi.PrEmsAbPublicDelegates),
		mid:      boss.mid,
		entryIDs: [][]byte{{0x00, 0x01, 0x02}, make([]byte, 25)},
	}
	if code := s.modLinkAttCore(garbage, "boss@hermex.test"); code != ecSuccess {
		t.Errorf("garbage entry ids = %#x, want ecSuccess (skipped)", code)
	}
	if n := len(w.delegates["boss@hermex.test"]); n != 0 {
		t.Errorf("garbage entry ids added %d delegates, want 0", n)
	}
}
