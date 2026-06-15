package nspi

import (
	"testing"

	"hermex/internal/ext"
	"hermex/internal/mapi"
)

// anrFilter builds the PR_ANR restriction Outlook sends to GetMatches as an
// alternative to ResolveNamesW.
func anrFilter(token string) mapi.Restriction {
	return mapi.Restriction{
		Type: mapi.ResProperty,
		Value: mapi.PropertyRestriction{
			Relop:   mapi.RelopEQ,
			PropTag: mapi.PrAnr,
			PropVal: mapi.TaggedPropVal{Tag: mapi.PrAnr, Value: token},
		},
	}
}

// buildGetMatches frames a GetMatches request: reserved1 + a STAT + an (empty)
// input-MId list + a reserved word + an optional filter + no property name + the
// row-count cap + an optional column set + an empty auxiliary buffer.
func buildGetMatches(st stat, filter *mapi.Restriction, rowCount uint32, cols []mapi.PropTag) []byte {
	p := ext.NewPush(abkFlags)
	p.Uint32(0) // reserved1
	p.Uint8(1)  // hasStat
	pushStat(p, st)
	p.Uint8(0)  // hasInMids = 0
	p.Uint32(0) // reserved
	if filter == nil {
		p.Uint8(0)
	} else {
		p.Uint8(1)
		_ = p.Restriction(*filter)
	}
	p.Uint8(0) // hasPropName = 0
	p.Uint32(rowCount)
	if cols == nil {
		p.Uint8(0)
	} else {
		p.Uint8(1)
		_ = p.PropTagsLong(cols)
	}
	p.Uint32(0) // cb_auxin
	return p.Bytes()
}

// decodeGetMatches reads a GetMatches response: result, the always-present STAT,
// the matched MIds, and the projected rows.
func decodeGetMatches(t *testing.T, resp []byte) (result uint32, mids []uint32, st stat, rows []mapi.PropertyValues) {
	t.Helper()
	p := ext.NewPull(resp, abkFlags)
	if status := mustU32(t, p, "status"); status != 0 {
		t.Fatalf("status = %#x, want 0", status)
	}
	result = mustU32(t, p, "result")
	if m := mustU8(t, p, "STAT marker"); m != 0xFF {
		t.Fatalf("STAT marker = %#x, want 0xFF (always present)", m)
	}
	var err error
	if st, err = pullStat(p); err != nil {
		t.Fatal(err)
	}
	if mustU8(t, p, "mids marker") == 0 {
		mustU8(t, p, "row set marker") // failure carries a second 0
		return result, nil, st, nil
	}
	mtags, err := p.PropTagsLong()
	if err != nil {
		t.Fatal(err)
	}
	mids = make([]uint32, len(mtags))
	for i, m := range mtags {
		mids[i] = uint32(m)
	}
	mustU8(t, p, "row set marker")
	cols, err := p.PropTagsLong()
	if err != nil {
		t.Fatal(err)
	}
	n := mustU32(t, p, "row count")
	for range n {
		rows = append(rows, decodeRow(t, p, cols))
	}
	return result, mids, st, rows
}

// TestGetMatchesAnr proves a PR_ANR restriction matches by SMTP/display
// substring, returns the matched MId, projects its row, and reports the
// bookmark (container_id := cur_rec) in the updated STAT.
func TestGetMatchesAnr(t *testing.T) {
	s := testGAL("alice@hermex.test", "bob@hermex.test")
	f := anrFilter("alice")
	result, mids, st, rows := decodeGetMatches(t, s.GetMatches(buildGetMatches(
		stat{sortType: sortTypeDisplayName, curRec: midBeginningOfTable, codePage: 1252}, &f, 50, nil)))
	if result != ecSuccess {
		t.Fatalf("result = %#x, want ecSuccess", result)
	}
	if len(mids) != 1 || mids[0] != midBase {
		t.Fatalf("mids = %#x, want [%#x] (alice)", mids, midBase)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	if v, _ := rows[0].Get(mapi.PrSmtpAddress); v != "alice@hermex.test" {
		t.Errorf("row SMTP = %v, want alice", v)
	}
	if st.containerID != st.curRec {
		t.Errorf("container_id = %#x, want cur_rec %#x (bookmark, point 16)", st.containerID, st.curRec)
	}
}

// TestGetMatchesRowCountCap proves the row-count cap bounds the match set: a
// token matching every entry returns only the first `requested` MIds.
func TestGetMatchesRowCountCap(t *testing.T) {
	s := testGAL("alice@hermex.test", "bob@hermex.test", "carol@hermex.test")
	f := anrFilter("@hermex.test") // matches all three
	_, mids, _, _ := decodeGetMatches(t, s.GetMatches(buildGetMatches(
		stat{sortType: sortTypeDisplayName, codePage: 1252}, &f, 2, nil)))
	if len(mids) != 2 {
		t.Errorf("mids = %d, want 2 (capped at rowCount)", len(mids))
	}
}

// TestGetMatchesExistAll proves a RES_EXIST{PR_ENTRYID} restriction yields the
// whole GAL, the documented equivalent of iterating with QueryRows.
func TestGetMatchesExistAll(t *testing.T) {
	s := testGAL("alice@hermex.test", "bob@hermex.test", "carol@hermex.test")
	f := mapi.Restriction{Type: mapi.ResExist, Value: mapi.ExistRestriction{PropTag: mapi.PrEntryID}}
	_, mids, _, _ := decodeGetMatches(t, s.GetMatches(buildGetMatches(
		stat{sortType: sortTypeDisplayName, codePage: 1252}, &f, 50, nil)))
	if len(mids) != 3 {
		t.Errorf("mids = %d, want 3 (all entries exist)", len(mids))
	}
}

// TestGetMatchesAnrEqualsResolve encodes the invariant the protocol relies on:
// for a uniquely-resolving token, GetMatches and ResolveNamesW agree on the
// matched entry, because both run the same matchesToken predicate.
func TestGetMatchesAnrEqualsResolve(t *testing.T) {
	s := testGAL("alice@hermex.test", "bob@hermex.test", "carol@hermex.test")
	const token = "carol"
	f := anrFilter(token)
	_, mids, _, _ := decodeGetMatches(t, s.GetMatches(buildGetMatches(
		stat{sortType: sortTypeDisplayName, codePage: 1252}, &f, 50, nil)))

	wantMID, status := s.snapshot().resolve(token)
	if status != midResolved {
		t.Fatalf("resolve(%q) status = %#x, want midResolved", token, status)
	}
	if len(mids) != 1 || mids[0] != wantMID {
		t.Errorf("GetMatches mids = %#x, want [%#x] (must agree with resolve)", mids, wantMID)
	}
}

// TestGetMatchesSortTypeRejected proves a non-display-name sort is unsupported.
func TestGetMatchesSortTypeRejected(t *testing.T) {
	s := testGAL("alice@hermex.test")
	f := anrFilter("alice")
	result, _, _, _ := decodeGetMatches(t, s.GetMatches(buildGetMatches(
		stat{sortType: 0x99, codePage: 1252}, &f, 50, nil)))
	if result != ecNotSupported {
		t.Errorf("result = %#x, want ecNotSupported", result)
	}
}

// TestGetMatchesUnicodeRejected proves a CP_WINUNICODE GetMatches is refused.
func TestGetMatchesUnicodeRejected(t *testing.T) {
	s := testGAL("alice@hermex.test")
	f := anrFilter("alice")
	result, _, _, _ := decodeGetMatches(t, s.GetMatches(buildGetMatches(
		stat{sortType: sortTypeDisplayName, codePage: cpWinUnicode}, &f, 50, nil)))
	if result != ecNotSupported {
		t.Errorf("result = %#x, want ecNotSupported", result)
	}
}

// TestMatchNodeLogic unit-tests the boolean restriction combinators against a
// single user: AND requires all, OR requires any, NOT inverts.
func TestMatchNodeLogic(t *testing.T) {
	u := galUser{mid: midBase, display: "Alice Smith", smtp: "alice@hermex.test"}
	hit := anrFilter("alice")
	miss := anrFilter("zzz")

	cases := []struct {
		name string
		res  mapi.Restriction
		want bool
	}{
		{"and-both", mapi.Restriction{Type: mapi.ResAnd, Value: []mapi.Restriction{hit, hit}}, true},
		{"and-one-miss", mapi.Restriction{Type: mapi.ResAnd, Value: []mapi.Restriction{hit, miss}}, false},
		{"or-one-hit", mapi.Restriction{Type: mapi.ResOr, Value: []mapi.Restriction{miss, hit}}, true},
		{"or-all-miss", mapi.Restriction{Type: mapi.ResOr, Value: []mapi.Restriction{miss, miss}}, false},
		{"not-miss", mapi.Restriction{Type: mapi.ResNot, Value: miss}, true},
		{"not-hit", mapi.Restriction{Type: mapi.ResNot, Value: hit}, false},
	}
	for _, c := range cases {
		if got := matchNode(u, &c.res); got != c.want {
			t.Errorf("%s: matchNode = %v, want %v", c.name, got, c.want)
		}
	}
}
