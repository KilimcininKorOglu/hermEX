package nspi

import (
	"fmt"
	"testing"

	"hermex/internal/ext"
	"hermex/internal/mapi"
)

// buildDNToMId frames a DNToMId request over a String8 DN array.
func buildDNToMId(dns ...string) []byte {
	p := ext.NewPush(abkFlags)
	p.Uint32(0) // reserved
	p.Uint8(1)  // hasNames
	p.Uint32(uint32(len(dns)))
	for _, dn := range dns {
		p.String8(dn)
	}
	p.Uint32(0) // cb_auxin
	return p.Bytes()
}

// buildResolveNames frames a ResolveNamesW request: reserved + STAT + no columns
// (use default) + a UTF-16 name array.
func buildResolveNames(codePage uint32, names ...string) []byte {
	p := ext.NewPush(abkFlags)
	p.Uint32(0) // reserved
	p.Uint8(1)  // hasStat
	pushStat(p, stat{codePage: codePage})
	p.Uint8(0) // hasColumns = 0
	p.Uint8(1) // hasNames
	p.Uint32(uint32(len(names)))
	for _, n := range names {
		p.Unicode(n)
	}
	p.Uint32(0) // cb_auxin
	return p.Bytes()
}

// TestDNToMId proves a known DN resolves to its MId and an unknown DN (or a
// non-DN string) yields MID_UNRESOLVED.
func TestDNToMId(t *testing.T) {
	s := testGAL("alice@hermex.test", "bob@hermex.test", "carol@hermex.test")
	body := buildDNToMId(userDN("bob@hermex.test"), userDN("nobody@x.test"), "not-a-dn")
	p := ext.NewPull(s.DNToMId(body, testCaller), abkFlags)
	mustU32(t, p, "status")
	if r := mustU32(t, p, "result"); r != ecSuccess {
		t.Fatalf("result = %#x", r)
	}
	if m := mustU8(t, p, "marker"); m != 0xFF {
		t.Fatalf("outmids marker = %#x, want 0xFF", m)
	}
	mids, err := p.PropTagsLong()
	if err != nil {
		t.Fatal(err)
	}
	if len(mids) != 3 {
		t.Fatalf("outmids = %d, want 3", len(mids))
	}
	if uint32(mids[0]) != midBase+1 {
		t.Errorf("bob DN -> %#x, want %#x", uint32(mids[0]), midBase+1)
	}
	if uint32(mids[1]) != midUnresolved {
		t.Errorf("unknown DN -> %#x, want MID_UNRESOLVED", uint32(mids[1]))
	}
	if uint32(mids[2]) != midUnresolved {
		t.Errorf("non-DN -> %#x, want MID_UNRESOLVED", uint32(mids[2]))
	}
}

// TestResolveNamesW proves the per-name resolution codes (resolved / ambiguous /
// unresolved), the "TYPE:" prefix strip, and that a row is returned for each
// uniquely resolved name.
func TestResolveNamesW(t *testing.T) {
	s := testGAL("alice@hermex.test", "bob@hermex.test", "carol@hermex.test")
	body := buildResolveNames(1252,
		"bob@hermex.test",        // unique -> resolved
		"hermex.test",            // matches all three -> ambiguous
		"nobody",                 // no match -> unresolved
		"SMTP:carol@hermex.test", // prefix-stripped, unique -> resolved
	)
	p := ext.NewPull(s.ResolveNamesW(body, testCaller), abkFlags)
	mustU32(t, p, "status")
	wantEq(t, "the result", mustU32(t, p, "result"), uint32(ecSuccess))
	wantEq(t, "the echoed code page", mustU32(t, p, "codepage"), uint32(1252))
	wantEq(t, "the mids marker", mustU8(t, p, "mids marker"), uint8(0xFF))

	mids, err := p.PropTagsLong()
	mustNoErr(t, "decode the mids", err)
	want := []uint32{midResolved, midAmbiguous, midUnresolved, midResolved}
	if len(mids) != len(want) {
		t.Fatalf("mids = %d, want %d", len(mids), len(want))
	}
	for i, w := range want {
		wantEq(t, fmt.Sprintf("mids[%d]", i), uint32(mids[i]), w)
	}

	wantEq(t, "the rows marker", mustU8(t, p, "rows marker"), uint8(0xFF))
	cols, err := p.PropTagsLong()
	mustNoErr(t, "decode the columns", err)
	wantEq(t, "resolved rows (bob + carol)", mustU32(t, p, "row count"), uint32(2))
	row0 := decodeRow(t, p, cols)
	v, _ := row0.Get(mapi.PrSmtpAddress)
	wantEq(t, "the first resolved row's SMTP address", v, any("bob@hermex.test"))
}
