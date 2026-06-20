package nspi

import (
	"testing"

	"hermex/internal/directory"
	"hermex/internal/mapi"
)

// maskedGAL is a directory.GAL whose entries carry an address-book hide mask.
// snapshot() always queries with an empty string, so the query is ignored and
// every entry is returned; the NSPI layer does the per-surface hide filtering.
type maskedGAL []directory.GALEntry

func (m maskedGAL) SearchGAL(query string, limit int) ([]directory.GALEntry, error) {
	return []directory.GALEntry(m), nil
}

// hidingServer builds a Server over four users that pin every hide combination:
// "both" is hidden from the GAL and resolution, "gal" only from the GAL, "resol"
// only from resolution, "vis" from nothing. Display name equals the address, so
// the snapshot order is both<gal<resol<vis with MIds 0x10..0x13.
func hidingServer() *Server {
	return NewServer(maskedGAL{
		{DisplayName: "both@hermex.test", Address: "both@hermex.test", HiddenFrom: abHideFromGAL | abHideResolve},
		{DisplayName: "gal@hermex.test", Address: "gal@hermex.test", HiddenFrom: abHideFromGAL},
		{DisplayName: "resol@hermex.test", Address: "resol@hermex.test", HiddenFrom: abHideResolve},
		{DisplayName: "vis@hermex.test", Address: "vis@hermex.test", HiddenFrom: 0},
	}, testGUID)
}

func rowSMTP(t *testing.T, row mapi.PropertyValues) string {
	t.Helper()
	v, ok := row.Get(mapi.PrSmtpAddress)
	if !ok {
		t.Fatalf("row has no PR_SMTP_ADDRESS: %+v", row)
	}
	s, _ := v.(string)
	return s
}

// TestSnapshotCarriesHideMask proves the directory's hide mask reaches the GAL
// entry, so every downstream surface can apply its own bit.
func TestSnapshotCarriesHideMask(t *testing.T) {
	g := hidingServer().snapshot()
	want := map[string]uint32{
		"both@hermex.test":  abHideFromGAL | abHideResolve,
		"gal@hermex.test":   abHideFromGAL,
		"resol@hermex.test": abHideResolve,
		"vis@hermex.test":   0,
	}
	for _, u := range g.users {
		if u.hidden != want[u.smtp] {
			t.Errorf("%s hidden = %#x, want %#x", u.smtp, u.hidden, want[u.smtp])
		}
	}
}

// TestQueryRowsHidesFromGAL proves a GAL-browse walk drops the AB_HIDE_FROM_GAL
// users (both, gal) and keeps the rest, with total_rec counting only the visible
// rows — yet the hidden user is still fetchable by an explicit MId, because
// asking for a specific entry opens it regardless of the GAL bit.
func TestQueryRowsHidesFromGAL(t *testing.T) {
	s := hidingServer()
	r := s.queryRowsCore(queryRowsRequest{stat: stat{codePage: 1252}, count: 100})
	if r.result != ecSuccess {
		t.Fatalf("result = %#x, want ecSuccess", r.result)
	}
	got := []string{}
	for _, row := range r.rows {
		got = append(got, rowSMTP(t, row))
	}
	want := []string{"resol@hermex.test", "vis@hermex.test"}
	if len(got) != len(want) {
		t.Fatalf("browse rows = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("browse row[%d] = %q, want %q", i, got[i], want[i])
		}
	}
	if r.stat.totalRec != 2 {
		t.Errorf("total_rec = %d, want 2 (visible rows only)", r.stat.totalRec)
	}

	// gal@ (MId 0x11) is hidden from the GAL browse but a direct fetch by its MId
	// still returns it.
	galMID := midBase + 1
	d := s.queryRowsCore(queryRowsRequest{stat: stat{codePage: 1252}, explicit: []uint32{galMID}, count: 100})
	if len(d.rows) != 1 || rowSMTP(t, d.rows[0]) != "gal@hermex.test" {
		t.Errorf("explicit fetch of hidden gal@ = %v, want one gal@ row", d.rows)
	}
}

// TestResolveIndependentOfGALBit is the load-bearing independence proof: a user
// hidden only from the GAL (gal@) is still resolvable by name, while a user
// hidden from resolution (resol@, both@) is not. If the GAL bit ever silently
// blocked resolution, gal@ would fail here.
func TestResolveIndependentOfGALBit(t *testing.T) {
	g := hidingServer().snapshot()
	cases := []struct {
		token      string
		wantStatus uint32
		wantSMTP   string
	}{
		{"gal", midResolved, "gal@hermex.test"}, // GAL-hidden but still resolvable
		{"vis", midResolved, "vis@hermex.test"},
		{"resol", midUnresolved, ""},
		{"both", midUnresolved, ""},
	}
	for _, c := range cases {
		mid, status := g.resolve(c.token)
		if status != c.wantStatus {
			t.Errorf("resolve(%q) status = %#x, want %#x", c.token, status, c.wantStatus)
			continue
		}
		if c.wantSMTP != "" {
			if u, ok := g.byMID(mid); !ok || u.smtp != c.wantSMTP {
				t.Errorf("resolve(%q) MId = %#x (%q), want %q", c.token, mid, u.smtp, c.wantSMTP)
			}
		}
	}
}

// TestGetMatchesHidesGALandResolve proves the GAL-container GetMatches query
// honors both the GAL and the resolution hide bits, so only the fully visible
// user matches an ANR token that otherwise matches everyone.
func TestGetMatchesHidesGALandResolve(t *testing.T) {
	s := hidingServer()
	f := anrFilter("@hermex.test") // matches all four addresses
	_, mids, _, rows := decodeGetMatches(t, s.GetMatches(buildGetMatches(
		stat{sortType: sortTypeDisplayName, codePage: 1252}, &f, 50, nil)))
	if len(mids) != 1 {
		t.Fatalf("mids = %d (%v), want 1 (only vis@)", len(mids), mids)
	}
	if rowSMTP(t, rows[0]) != "vis@hermex.test" {
		t.Errorf("matched %q, want vis@hermex.test", rowSMTP(t, rows[0]))
	}
}

// TestSeekEntriesSkipsGALHidden proves a GAL seek lands on the first visible
// entry at or after the target: seeking "g" would land on gal@ in the full list,
// but gal@ is hidden from the GAL, so the cursor advances to resol@.
func TestSeekEntriesSkipsGALHidden(t *testing.T) {
	s := hidingServer()
	req := seekEntriesRequest{
		stat:   stat{sortType: sortTypeDisplayName, codePage: 1252},
		target: mapi.TaggedPropVal{Tag: mapi.PrDisplayName, Value: "g"},
	}
	r := s.seekEntriesCore(req)
	if r.result != ecSuccess {
		t.Fatalf("result = %#x, want ecSuccess", r.result)
	}
	if got := rowSMTP(t, r.rows[0]); got != "resol@hermex.test" {
		t.Errorf("seek(\"g\") landed on %q, want resol@hermex.test (gal@ is GAL-hidden)", got)
	}
	if r.stat.totalRec != 2 {
		t.Errorf("total_rec = %d, want 2 (visible rows only)", r.stat.totalRec)
	}
}

// TestDistlistRendersAsDistList proves a GAL entry that is a distribution list
// carries the DT_DISTLIST object/display type (and not the mailuser default), so
// a client shows it as a list it can expand rather than a person.
func TestDistlistRendersAsDistList(t *testing.T) {
	s := NewServer(maskedGAL{
		{DisplayName: "team", Address: "team@hermex.test", DisplayType: mapi.DisplayTypeDistList},
		{DisplayName: "alice", Address: "alice@hermex.test", DisplayType: mapi.DisplayTypeMailUser},
	}, testGUID)
	g := s.snapshot()
	props := map[string]mapi.PropertyValues{}
	for _, u := range g.users {
		props[u.smtp] = galUserProps(u)
	}
	if ot, _ := props["team@hermex.test"].Get(mapi.PrObjectType); ot != int32(mapi.ObjectTypeDistList) {
		t.Errorf("list object type = %v, want %d (distlist)", ot, mapi.ObjectTypeDistList)
	}
	if dt, _ := props["team@hermex.test"].Get(mapi.PrDisplayType); dt != int32(mapi.DisplayTypeDistList) {
		t.Errorf("list display type = %v, want %d (distlist)", dt, mapi.DisplayTypeDistList)
	}
	if ot, _ := props["alice@hermex.test"].Get(mapi.PrObjectType); ot != int32(mapi.ObjectTypeMailUser) {
		t.Errorf("user object type = %v, want %d (mailuser)", ot, mapi.ObjectTypeMailUser)
	}
}

// TestGetPropsDirectOpenIgnoresHide proves a direct GetProps by a hidden user's
// cursor still returns its row: hiding governs browse and resolution, not the
// ability to open a specific entry the client already addresses.
func TestGetPropsDirectOpenIgnoresHide(t *testing.T) {
	s := hidingServer()
	bothMID := midBase // both@ is first, fully hidden
	_, row := decodeGetProps(t, s.GetProps(buildGetProps(
		stat{sortType: sortTypeDisplayName, codePage: 1252, curRec: bothMID}, nil)))
	if got := rowSMTP(t, row); got != "both@hermex.test" {
		t.Errorf("direct GetProps of hidden both@ = %q, want both@hermex.test", got)
	}
}
