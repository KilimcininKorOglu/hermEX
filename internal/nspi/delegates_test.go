package nspi

import (
	"slices"
	"strings"
	"testing"

	"hermex/internal/mapi"
)

// delegatingGAL is a maskedGAL that also answers public-delegate reads, so the
// PR_EMS_AB_PUBLIC_DELEGATES container can be exercised without a real mailbox
// store. The map is keyed by the delegator's SMTP (case-insensitive).
type delegatingGAL struct {
	maskedGAL
	delegates map[string][]string
}

func (d delegatingGAL) Delegates(userAddr string) ([]string, error) {
	return d.delegates[strings.ToLower(userAddr)], nil
}

// TestGetMatchesPublicDelegatesReadsListAndHidesDelegate proves the
// public-delegates container reads the delegator's list at cur_rec, drops a
// delegate hidden from the delegate list (0x04) and a delegate absent from the
// GAL, and keeps the rest. The 0x04 bit is independent: a delegate hidden only
// from the GAL (0x01) still appears as someone's delegate.
func TestGetMatchesPublicDelegatesReadsListAndHidesDelegate(t *testing.T) {
	s := NewServer(delegatingGAL{
		maskedGAL: maskedGAL{
			{DisplayName: "boss@hermex.test", Address: "boss@hermex.test", DisplayType: rtUser},
			{DisplayName: "alice@hermex.test", Address: "alice@hermex.test", DisplayType: rtUser},
			{DisplayName: "bob@hermex.test", Address: "bob@hermex.test", DisplayType: rtUser, HiddenFrom: abHideDelegate},
			{DisplayName: "carol@hermex.test", Address: "carol@hermex.test", DisplayType: rtUser, HiddenFrom: abHideFromGAL},
		},
		delegates: map[string][]string{
			"boss@hermex.test": {"alice@hermex.test", "bob@hermex.test", "carol@hermex.test", "ghost@other.test"},
		},
	}, testGUID)

	g := s.snapshot()
	boss, ok := g.userByAddress("boss@hermex.test")
	if !ok {
		t.Fatal("boss not in GAL")
	}
	r := s.getMatchesCore(getMatchesRequest{
		stat:     stat{sortType: sortTypeDisplayName, codePage: 1252, containerID: uint32(mapi.PrEmsAbPublicDelegates), curRec: boss.mid},
		rowCount: 50,
	})
	if r.result != ecSuccess {
		t.Fatalf("result = %#x, want ecSuccess", r.result)
	}
	var got []string
	for _, row := range r.rows {
		if v, ok := row.Get(mapi.PrSmtpAddress); ok {
			got = append(got, v.(string))
		}
	}
	slices.Sort(got)
	// bob dropped (0x04), ghost dropped (not in GAL); alice + carol (GAL-hidden) kept.
	want := []string{"alice@hermex.test", "carol@hermex.test"}
	if !slices.Equal(got, want) {
		t.Errorf("public delegates = %v, want %v", got, want)
	}
}

// TestGetMatchesPublicDelegatesWithoutReaderIsEmpty proves a directory that does
// not expose delegates (the static/test directory) yields an empty list rather
// than an error: the container is simply unpopulated.
func TestGetMatchesPublicDelegatesWithoutReaderIsEmpty(t *testing.T) {
	s := NewServer(maskedGAL{
		{DisplayName: "boss@hermex.test", Address: "boss@hermex.test", DisplayType: rtUser},
	}, testGUID)
	g := s.snapshot()
	boss, _ := g.userByAddress("boss@hermex.test")
	r := s.getMatchesCore(getMatchesRequest{
		stat:     stat{sortType: sortTypeDisplayName, codePage: 1252, containerID: uint32(mapi.PrEmsAbPublicDelegates), curRec: boss.mid},
		rowCount: 50,
	})
	if r.result != ecSuccess {
		t.Fatalf("result = %#x, want ecSuccess", r.result)
	}
	if len(r.mids) != 0 {
		t.Errorf("public delegates without a reader = %d mids, want 0", len(r.mids))
	}
}
