package nspi

import (
	"slices"
	"testing"

	"hermex/internal/mapi"
)

// browseList walks a container with QueryRows from the table start and returns
// the SMTP addresses of the rows, in browse order.
func browseList(t *testing.T, s *Server, containerID uint32) []string {
	t.Helper()
	r := s.queryRowsCore(queryRowsRequest{
		stat:  stat{sortType: sortTypeDisplayName, codePage: 1252, containerID: containerID, curRec: midBeginningOfTable},
		count: 100,
	})
	if r.result != ecSuccess {
		t.Fatalf("QueryRows(container %#x) result = %#x, want ecSuccess", containerID, r.result)
	}
	var out []string
	for _, row := range r.rows {
		if v, ok := row.Get(mapi.PrSmtpAddress); ok {
			out = append(out, v.(string))
		}
	}
	return out
}

// typedGAL builds a Server over one recipient of each address-book display type,
// display name = address so the snapshot order is deterministic.
func typedGAL() *Server {
	return NewServer(maskedGAL{
		{DisplayName: "user@hermex.test", Address: "user@hermex.test", DisplayType: rtUser},
		{DisplayName: "list@hermex.test", Address: "list@hermex.test", DisplayType: rtDistList},
		{DisplayName: "contact@hermex.test", Address: "contact@hermex.test", DisplayType: rtContact},
		{DisplayName: "room@hermex.test", Address: "room@hermex.test", DisplayType: rtRoom},
		{DisplayName: "kit@hermex.test", Address: "kit@hermex.test", DisplayType: rtEquipment},
	}, testGUID)
}

// TestNamedListsClassifyByType proves each named container browses exactly the
// recipients of its display type, and the GAL (container 0) browses them all.
func TestNamedListsClassifyByType(t *testing.T) {
	s := typedGAL()
	cases := []struct {
		container uint32
		want      string
	}{
		{uint32(alContainerUsers), "user@hermex.test"},
		{uint32(alContainerDistLists), "list@hermex.test"},
		{uint32(alContainerContacts), "contact@hermex.test"},
		{uint32(alContainerRooms), "room@hermex.test"},
		{uint32(alContainerEquipment), "kit@hermex.test"},
	}
	for _, c := range cases {
		got := browseList(t, s, c.container)
		if !slices.Equal(got, []string{c.want}) {
			t.Errorf("container %#x browse = %v, want [%s]", c.container, got, c.want)
		}
	}
	// The GAL holds every type.
	gal := browseList(t, s, uint32(galContainerID))
	if len(gal) != 5 {
		t.Errorf("GAL browse = %d rows, want 5 (every type)", len(gal))
	}
}

// TestNamedListBrowseHidesFromALButShowsGALHidden pins the two independent hide
// bits on the named-list browse surface: a user hidden from address lists (0x02)
// is dropped, while a user hidden only from the GAL (0x01) is still shown. The
// complementary GAL browse drops the GAL-hidden one and shows the AL-hidden one.
func TestNamedListBrowseHidesFromALButShowsGALHidden(t *testing.T) {
	s := NewServer(maskedGAL{
		{DisplayName: "a-plain", Address: "plain@hermex.test", DisplayType: rtUser},
		{DisplayName: "b-galhidden", Address: "galhidden@hermex.test", DisplayType: rtUser, HiddenFrom: abHideFromGAL},
		{DisplayName: "c-alhidden", Address: "alhidden@hermex.test", DisplayType: rtUser, HiddenFrom: abHideFromAL},
	}, testGUID)

	// All Users: drops the AL-hidden user, keeps the GAL-hidden one.
	if got := browseList(t, s, uint32(alContainerUsers)); !slices.Equal(got, []string{"plain@hermex.test", "galhidden@hermex.test"}) {
		t.Errorf("All Users browse = %v, want [plain galhidden] (AL-hidden dropped, GAL-hidden shown)", got)
	}
	// GAL: drops the GAL-hidden user, keeps the AL-hidden one.
	if got := browseList(t, s, uint32(galContainerID)); !slices.Equal(got, []string{"plain@hermex.test", "alhidden@hermex.test"}) {
		t.Errorf("GAL browse = %v, want [plain alhidden] (GAL-hidden dropped, AL-hidden shown)", got)
	}
}

// TestContainerIDDoesNotCrossTalkWithMID proves a STAT container_id never
// redirects a by-MId fetch: GetProps on a room's entry MId returns that room even
// when the STAT selects the All Users container. container_id can carry an entry
// MId (GetMatches copies cur_rec into it), so the two fields must stay disjoint.
func TestContainerIDDoesNotCrossTalkWithMID(t *testing.T) {
	s := NewServer(maskedGAL{
		{DisplayName: "alice@hermex.test", Address: "alice@hermex.test", DisplayType: rtUser},
		{DisplayName: "boardroom@hermex.test", Address: "boardroom@hermex.test", DisplayType: rtRoom},
	}, testGUID)
	// alice < boardroom by display name, so boardroom is MId midBase+1.
	st := stat{curRec: midBase + 1, codePage: 1252, containerID: uint32(alContainerUsers)}
	_, row := decodeGetProps(t, s.GetProps(buildGetProps(st, nil)))
	if got := rowSMTP(t, row); got != "boardroom@hermex.test" {
		t.Errorf("by-MId GetProps under the All Users container = %q, want boardroom (no cross-talk)", got)
	}
}

// TestUnknownContainerBrowsesNothing proves an unrecognized container id (here an
// entry MId, which a stale cursor could carry) browses an empty table rather than
// silently falling back to the GAL.
func TestUnknownContainerBrowsesNothing(t *testing.T) {
	s := NewServer(maskedGAL{
		{DisplayName: "alice@hermex.test", Address: "alice@hermex.test", DisplayType: rtUser},
	}, testGUID)
	if got := browseList(t, s, midBase); len(got) != 0 {
		t.Errorf("browse of an entry-MId container = %v, want empty", got)
	}
}

// TestGetMatchesInNamedListFiltersTypeAndHide proves an ANR search inside a named
// list keeps only the list's recipient type and honors the address-list and
// name-resolution hide bits: a matching room is excluded by type, a member hidden
// from address lists (0x02) or from resolution (0x08) is dropped, but a member
// hidden only from the GAL (0x01) is still matched (the bits are independent).
func TestGetMatchesInNamedListFiltersTypeAndHide(t *testing.T) {
	s := NewServer(maskedGAL{
		{DisplayName: "team-alice", Address: "alice@hermex.test", DisplayType: rtUser},
		{DisplayName: "team-room", Address: "alice-room@hermex.test", DisplayType: rtRoom},
		{DisplayName: "team-galhid", Address: "alice-gal@hermex.test", DisplayType: rtUser, HiddenFrom: abHideFromGAL},
		{DisplayName: "team-alhid", Address: "alice-al@hermex.test", DisplayType: rtUser, HiddenFrom: abHideFromAL},
		{DisplayName: "team-reshid", Address: "alice-res@hermex.test", DisplayType: rtUser, HiddenFrom: abHideResolve},
	}, testGUID)
	f := anrFilter("alice")
	r := s.getMatchesCore(getMatchesRequest{
		stat:     stat{sortType: sortTypeDisplayName, codePage: 1252, containerID: uint32(alContainerUsers), curRec: midBeginningOfTable},
		filter:   &f,
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
	// Room excluded by type; AL- and resolve-hidden dropped; GAL-hidden kept.
	want := []string{"alice-gal@hermex.test", "alice@hermex.test"}
	if !slices.Equal(got, want) {
		t.Errorf("All Users GetMatches = %v, want %v", got, want)
	}
}
