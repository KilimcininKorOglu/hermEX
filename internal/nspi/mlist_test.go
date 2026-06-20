package nspi

import (
	"slices"
	"strings"
	"testing"

	"hermex/internal/directory"
	"hermex/internal/mapi"
)

// memberGAL is a directory.GAL that also expands distribution lists, for
// exercising NSPI member expansion. SearchGAL returns the entries; ExpandMList
// returns a configured list's members (ignoring the privilege, since the
// address-book bypass passes from == listaddr).
type memberGAL struct {
	entries []directory.GALEntry
	members map[string][]string
}

func (m memberGAL) SearchGAL(string, int) ([]directory.GALEntry, error) {
	return m.entries, nil
}

func (m memberGAL) ExpandMList(listAddr, _ string) ([]string, directory.MListResult, error) {
	mem, ok := m.members[strings.ToLower(listAddr)]
	if !ok {
		return nil, directory.MListNone, nil
	}
	return mem, directory.MListOK, nil
}

// TestGetMatchesExpandsListMembers proves a GetMatches against the
// PR_EMS_AB_MEMBER container expands the distribution list at cur_rec into its
// members, dropping a member hidden from address lists (the member-expansion half
// of the 0x02 hide bit).
func TestGetMatchesExpandsListMembers(t *testing.T) {
	g := memberGAL{
		entries: []directory.GALEntry{
			{DisplayName: "team", Address: "team@hermex.test", DisplayType: mapi.DisplayTypeDistList},
			{DisplayName: "alice", Address: "alice@hermex.test"},
			{DisplayName: "bob", Address: "bob@hermex.test", HiddenFrom: 0x02}, // hidden from address lists
			{DisplayName: "carol", Address: "carol@hermex.test"},
		},
		members: map[string][]string{
			"team@hermex.test": {"alice@hermex.test", "bob@hermex.test", "carol@hermex.test"},
		},
	}
	s := NewServer(g, testGUID)
	snap := s.snapshot()
	teamMID, ok := snap.byAddress("team@hermex.test")
	if !ok {
		t.Fatal("team list not found in the GAL")
	}

	r := s.getMatchesCore(getMatchesRequest{
		stat: stat{
			sortType:    sortTypeDisplayName,
			codePage:    1252,
			containerID: uint32(mapi.PrEmsAbMember),
			curRec:      teamMID,
		},
	})
	if r.result != ecSuccess {
		t.Fatalf("result = %#x, want ecSuccess", r.result)
	}
	got := []string{}
	for _, mid := range r.mids {
		if u, ok := snap.byMID(mid); ok {
			got = append(got, u.smtp)
		}
	}
	slices.Sort(got)
	want := []string{"alice@hermex.test", "carol@hermex.test"} // bob dropped (hidden from AL)
	if !slices.Equal(got, want) {
		t.Errorf("expanded members = %v, want %v (bob is hidden from address lists)", got, want)
	}
}

// TestGetMatchesMemberOfNonList proves selecting the member container on an entry
// that is not a distribution list yields no members rather than an error.
func TestGetMatchesMemberOfNonList(t *testing.T) {
	g := memberGAL{
		entries: []directory.GALEntry{{DisplayName: "alice", Address: "alice@hermex.test"}},
	}
	s := NewServer(g, testGUID)
	snap := s.snapshot()
	aliceMID, _ := snap.byAddress("alice@hermex.test")

	r := s.getMatchesCore(getMatchesRequest{
		stat: stat{
			sortType:    sortTypeDisplayName,
			codePage:    1252,
			containerID: uint32(mapi.PrEmsAbMember),
			curRec:      aliceMID,
		},
	})
	if r.result != ecSuccess || len(r.mids) != 0 {
		t.Errorf("member expansion of a non-list = (%#x, %d mids), want (ecSuccess, 0)", r.result, len(r.mids))
	}
}
