package mapihttp

import (
	"bytes"
	"encoding/binary"
	"io"
	"net/http"
	"testing"
)

// bindBody frames a minimal NSPI Bind request: flags + no STAT + empty auxin.
func bindBody(flags uint32) []byte {
	var b []byte
	b = binary.LittleEndian.AppendUint32(b, flags) // Flags
	b = append(b, 0)                               // hasStat = 0 (default STAT)
	b = binary.LittleEndian.AppendUint32(b, 0)     // cb_auxin
	return b
}

// specialTableBody frames a minimal GetSpecialTable request: flags + no STAT +
// no version + empty auxin.
func specialTableBody() []byte {
	var b []byte
	b = binary.LittleEndian.AppendUint32(b, 0) // flags
	b = append(b, 0)                           // hasStat = 0
	b = append(b, 0)                           // hasVersion = 0
	b = binary.LittleEndian.AppendUint32(b, 0) // cb_auxin
	return b
}

// queryRowsBody frames a QueryRows request: flags + a STAT (cursor at the table
// start, code page 1252) + an empty explicit MId list + count + no columns +
// empty auxin.
func queryRowsBody() []byte {
	var b []byte
	b = binary.LittleEndian.AppendUint32(b, 0) // flags
	b = append(b, 1)                           // hasStat
	for i := 0; i < 9; i++ {                   // STAT: 9 u32 fields
		v := uint32(0)
		if i == 6 { // codepage
			v = 1252
		}
		b = binary.LittleEndian.AppendUint32(b, v)
	}
	b = binary.LittleEndian.AppendUint32(b, 0)  // explicit MId count = 0
	b = binary.LittleEndian.AppendUint32(b, 10) // count
	b = append(b, 0)                            // hasColumns = 0
	b = binary.LittleEndian.AppendUint32(b, 0)  // cb_auxin
	return b
}

// resolveNamesBody frames a ResolveNamesW request: reserved + a STAT (code page
// 1252) + no columns + the (ASCII) names as a UTF-16LE array.
func resolveNamesBody(names ...string) []byte {
	var b []byte
	b = binary.LittleEndian.AppendUint32(b, 0) // reserved
	b = append(b, 1)                           // hasStat
	for i := range 9 {                         // STAT
		v := uint32(0)
		if i == 6 { // codepage
			v = 1252
		}
		b = binary.LittleEndian.AppendUint32(b, v)
	}
	b = append(b, 0) // hasColumns = 0
	b = append(b, 1) // hasNames
	b = binary.LittleEndian.AppendUint32(b, uint32(len(names)))
	for _, n := range names {
		for _, c := range []byte(n) { // ASCII -> UTF-16LE
			b = append(b, c, 0)
		}
		b = append(b, 0, 0) // UTF-16 NUL terminator
	}
	b = binary.LittleEndian.AppendUint32(b, 0) // cb_auxin
	return b
}

// nspiPayload strips the chunked PROCESSING/DONE meta preamble and returns the
// NSPI response body bytes.
func nspiPayload(t *testing.T, resp *http.Response) []byte {
	t.Helper()
	body, _ := io.ReadAll(resp.Body)
	_, payload, found := bytes.Cut(body, []byte("\r\n\r\n"))
	if !found {
		t.Fatal("response missing meta preamble terminator")
	}
	return payload
}

// TestNspiBindUnbind drives the NSPI session lifecycle: Bind succeeds, sets the
// sid + sequence cookies, and frames a success result; Unbind needs the cookie
// and drops the session.
func TestNspiBindUnbind(t *testing.T) {
	ts := newTestServer(t)

	bind := mapiPost(t, ts, "/mapi/nspi", "Bind", bindBody(0), nil)
	defer bind.Body.Close()
	if got := bind.Header.Get("X-ResponseCode"); got != "0" {
		t.Fatalf("Bind: X-ResponseCode = %q, want 0", got)
	}
	sid := cookieByName(bind, "sid")
	if sid == "" || cookieByName(bind, "sequence") == "" {
		t.Fatal("Bind did not set sid + sequence cookies")
	}
	p := nspiPayload(t, bind)
	if len(p) < 28 {
		t.Fatalf("Bind response too short: %d bytes", len(p))
	}
	if status := binary.LittleEndian.Uint32(p[0:]); status != 0 {
		t.Errorf("Bind status = %#x, want 0", status)
	}
	if result := binary.LittleEndian.Uint32(p[4:]); result != 0 {
		t.Errorf("Bind result = %#x, want 0 (success)", result)
	}

	// Unbind without a cookie -> missing cookie (6).
	noCookie := mapiPost(t, ts, "/mapi/nspi", "Unbind", []byte{0, 0, 0, 0, 0, 0, 0, 0}, nil)
	noCookie.Body.Close()
	if got := noCookie.Header.Get("X-ResponseCode"); got != "6" {
		t.Errorf("Unbind without cookie: X-ResponseCode = %q, want 6", got)
	}

	// Unbind with the bound sid -> success.
	unb := mapiPost(t, ts, "/mapi/nspi", "Unbind", []byte{0, 0, 0, 0, 0, 0, 0, 0}, func(r *http.Request) {
		r.AddCookie(&http.Cookie{Name: "sid", Value: sid})
	})
	defer unb.Body.Close()
	if got := unb.Header.Get("X-ResponseCode"); got != "0" {
		t.Errorf("Unbind: X-ResponseCode = %q, want 0", got)
	}
}

// TestNspiBindAnonymousRejected proves an anonymous bind is framed at the
// transport (X-ResponseCode 0, the request was processed) but carries an
// AB-level failure result and opens no session.
func TestNspiBindAnonymousRejected(t *testing.T) {
	ts := newTestServer(t)
	bind := mapiPost(t, ts, "/mapi/nspi", "Bind", bindBody(0x20), nil) // fAnonymousLogin
	defer bind.Body.Close()
	if got := bind.Header.Get("X-ResponseCode"); got != "0" {
		t.Errorf("anonymous Bind: X-ResponseCode = %q, want 0 (processed)", got)
	}
	if cookieByName(bind, "sid") != "" {
		t.Error("anonymous Bind set a session cookie")
	}
	p := nspiPayload(t, bind)
	if len(p) < 8 {
		t.Fatalf("Bind response too short: %d bytes", len(p))
	}
	if result := binary.LittleEndian.Uint32(p[4:]); result == 0 {
		t.Error("anonymous Bind result = success, want a failure code")
	}
}

// TestNspiGetSpecialTable drives Bind then GetSpecialTable within the session:
// it needs the cookies, rolls the sequence, and returns the single GAL container
// row.
func TestNspiGetSpecialTable(t *testing.T) {
	ts := newTestServer(t)
	bind := mapiPost(t, ts, "/mapi/nspi", "Bind", bindBody(0), nil)
	bind.Body.Close()
	sid, seq := cookieByName(bind, "sid"), cookieByName(bind, "sequence")
	if sid == "" || seq == "" {
		t.Fatal("no cookies from Bind")
	}

	// Without cookies -> missing cookie (6).
	noCookie := mapiPost(t, ts, "/mapi/nspi", "GetSpecialTable", specialTableBody(), nil)
	noCookie.Body.Close()
	if got := noCookie.Header.Get("X-ResponseCode"); got != "6" {
		t.Errorf("GetSpecialTable without cookies: X-ResponseCode = %q, want 6", got)
	}

	// With the bound session -> success, sequence rolled, one container row.
	gst := mapiPost(t, ts, "/mapi/nspi", "GetSpecialTable", specialTableBody(), func(r *http.Request) {
		r.AddCookie(&http.Cookie{Name: "sid", Value: sid})
		r.AddCookie(&http.Cookie{Name: "sequence", Value: seq})
	})
	defer gst.Body.Close()
	if got := gst.Header.Get("X-ResponseCode"); got != "0" {
		t.Fatalf("GetSpecialTable: X-ResponseCode = %q, want 0", got)
	}
	if newSeq := cookieByName(gst, "sequence"); newSeq == "" || newSeq == seq {
		t.Errorf("GetSpecialTable did not roll the sequence (was %q, got %q)", seq, newSeq)
	}
	p := nspiPayload(t, gst)
	// status(0:4) + result(4:8) + codepage(8:12) + version-marker(12) + HasRows(13) + count(14:18)
	if len(p) < 18 {
		t.Fatalf("response too short: %d bytes", len(p))
	}
	if result := binary.LittleEndian.Uint32(p[4:]); result != 0 {
		t.Errorf("result = %#x, want 0", result)
	}
	if p[13] != 0xFF {
		t.Errorf("HasRows byte = %#x, want 0xFF", p[13])
	}
	if count := binary.LittleEndian.Uint32(p[14:]); count != 1 {
		t.Errorf("container row count = %d, want 1", count)
	}
}

// TestNspiQueryRows drives Bind then QueryRows within the session and confirms
// the transport round-trips a successful row set (the single seeded user).
func TestNspiQueryRows(t *testing.T) {
	ts := newTestServer(t)
	bind := mapiPost(t, ts, "/mapi/nspi", "Bind", bindBody(0), nil)
	bind.Body.Close()
	sid, seq := cookieByName(bind, "sid"), cookieByName(bind, "sequence")
	if sid == "" || seq == "" {
		t.Fatal("no cookies from Bind")
	}
	qr := mapiPost(t, ts, "/mapi/nspi", "QueryRows", queryRowsBody(), func(r *http.Request) {
		r.AddCookie(&http.Cookie{Name: "sid", Value: sid})
		r.AddCookie(&http.Cookie{Name: "sequence", Value: seq})
	})
	defer qr.Body.Close()
	if got := qr.Header.Get("X-ResponseCode"); got != "0" {
		t.Fatalf("QueryRows: X-ResponseCode = %q, want 0", got)
	}
	p := nspiPayload(t, qr)
	// status(0:4) + result(4:8) + STAT-marker(8) + STAT(9:45) + rows-marker(45)
	if len(p) < 46 {
		t.Fatalf("response too short: %d bytes", len(p))
	}
	if result := binary.LittleEndian.Uint32(p[4:]); result != 0 {
		t.Errorf("result = %#x, want 0", result)
	}
	if p[8] != 0xFF {
		t.Errorf("STAT marker = %#x, want 0xFF", p[8])
	}
	if p[45] != 0xFF {
		t.Errorf("rows marker = %#x, want 0xFF (a row set follows)", p[45])
	}
	// The display name must ride the wire as UTF-16LE, not UTF-8: the seeded
	// user's address appears with interleaved zero bytes. This checks the
	// address-book string encoding independent of our own encoder.
	u16 := []byte{'a', 0, 'l', 0, 'i', 0, 'c', 0, 'e', 0}
	if !bytes.Contains(p, u16) {
		t.Error("QueryRows response does not carry the display name as UTF-16LE")
	}
}

// TestNspiResolveNames drives Bind then ResolveNamesW within the session and
// confirms the route resolves the seeded user (the exact X-RequestType matters).
func TestNspiResolveNames(t *testing.T) {
	ts := newTestServer(t)
	bind := mapiPost(t, ts, "/mapi/nspi", "Bind", bindBody(0), nil)
	bind.Body.Close()
	sid, seq := cookieByName(bind, "sid"), cookieByName(bind, "sequence")
	if sid == "" || seq == "" {
		t.Fatal("no cookies from Bind")
	}
	rn := mapiPost(t, ts, "/mapi/nspi", "ResolveNamesW", resolveNamesBody("alice"), func(r *http.Request) {
		r.AddCookie(&http.Cookie{Name: "sid", Value: sid})
		r.AddCookie(&http.Cookie{Name: "sequence", Value: seq})
	})
	defer rn.Body.Close()
	if got := rn.Header.Get("X-ResponseCode"); got != "0" {
		t.Fatalf("ResolveNamesW: X-ResponseCode = %q, want 0", got)
	}
	p := nspiPayload(t, rn)
	// status(0:4) + result(4:8) + codepage(8:12) + mids-marker(12)
	if len(p) < 13 {
		t.Fatalf("response too short: %d bytes", len(p))
	}
	if result := binary.LittleEndian.Uint32(p[4:]); result != 0 {
		t.Errorf("result = %#x, want 0", result)
	}
	if p[12] != 0xFF {
		t.Errorf("mids marker = %#x, want 0xFF (a resolution follows)", p[12])
	}
}

// statBytes frames a STAT with the given cur_rec and code page 1252 (sort type 0
// = display name); the other fields are zero.
func statBytes(curRec uint32) []byte {
	var b []byte
	for i := range 9 {
		v := uint32(0)
		switch i {
		case 2: // cur_rec
			v = curRec
		case 6: // codepage
			v = 1252
		}
		b = binary.LittleEndian.AppendUint32(b, v)
	}
	return b
}

// getMatchesBody frames a GetMatches request with a PR_ANR restriction whose
// search token is encoded as UTF-16LE — a hand-built wire vector independent of
// our own encoder, so it proves the restriction's address-book string decodes
// correctly off the wire.
func getMatchesBody(token string) []byte {
	var b []byte
	b = binary.LittleEndian.AppendUint32(b, 0) // reserved1
	b = append(b, 1)                           // hasStat
	b = append(b, statBytes(0)...)             // cursor at table start
	b = append(b, 0)                           // hasInMids = 0
	b = binary.LittleEndian.AppendUint32(b, 0) // reserved
	b = append(b, 1)                           // hasFilter
	// RESTRICTION: ResProperty(0x04) + RelopEQ(0x04) + PR_ANR proptag + a
	// TaggedPropVal whose PtUnicode value carries the ABK present marker + UTF-16.
	b = append(b, 0x04)                                 // ResProperty
	b = append(b, 0x04)                                 // RelopEQ
	b = binary.LittleEndian.AppendUint32(b, 0x360A001F) // PR_ANR
	b = binary.LittleEndian.AppendUint32(b, 0x360A001F) // TaggedPropVal tag
	b = append(b, 0xFF)                                 // ABK value-present marker
	for _, c := range []byte(token) {                   // ASCII -> UTF-16LE
		b = append(b, c, 0)
	}
	b = append(b, 0, 0)                         // UTF-16 NUL terminator
	b = append(b, 0)                            // hasPropName = 0
	b = binary.LittleEndian.AppendUint32(b, 50) // rowCount
	b = append(b, 0)                            // hasColumns = 0
	b = binary.LittleEndian.AppendUint32(b, 0)  // cb_auxin
	return b
}

// TestNspiGetMatches drives Bind then GetMatches with a UTF-16 PR_ANR
// restriction and confirms the seeded user is matched and projected — the
// address-book acid test for the restriction's string encoding on the wire.
func TestNspiGetMatches(t *testing.T) {
	ts := newTestServer(t)
	bind := mapiPost(t, ts, "/mapi/nspi", "Bind", bindBody(0), nil)
	bind.Body.Close()
	sid, seq := cookieByName(bind, "sid"), cookieByName(bind, "sequence")
	if sid == "" || seq == "" {
		t.Fatal("no cookies from Bind")
	}
	gm := mapiPost(t, ts, "/mapi/nspi", "GetMatches", getMatchesBody("alice"), func(r *http.Request) {
		r.AddCookie(&http.Cookie{Name: "sid", Value: sid})
		r.AddCookie(&http.Cookie{Name: "sequence", Value: seq})
	})
	defer gm.Body.Close()
	if got := gm.Header.Get("X-ResponseCode"); got != "0" {
		t.Fatalf("GetMatches: X-ResponseCode = %q, want 0", got)
	}
	p := nspiPayload(t, gm)
	// status(0:4) + result(4:8) + STAT-marker(8) + STAT(9:45) + mids-marker(45)
	if len(p) < 46 {
		t.Fatalf("response too short: %d bytes", len(p))
	}
	if result := binary.LittleEndian.Uint32(p[4:]); result != 0 {
		t.Errorf("result = %#x, want 0", result)
	}
	if p[8] != 0xFF {
		t.Errorf("STAT marker = %#x, want 0xFF", p[8])
	}
	if p[45] != 0xFF {
		t.Errorf("mids marker = %#x, want 0xFF (a match follows)", p[45])
	}
	// The matched row carries alice as UTF-16LE: the UTF-16 ANR token decoded,
	// matched, and the row projected — all independent of our own encoder.
	if u16 := []byte{'a', 0, 'l', 0, 'i', 0, 'c', 0, 'e', 0}; !bytes.Contains(p, u16) {
		t.Error("GetMatches response does not carry the matched user as UTF-16LE")
	}
}

// TestNspiGetProps drives Bind then GetProps for the first GAL entry (cur_rec at
// the lowest entry MId) and confirms the route returns its property row.
func TestNspiGetProps(t *testing.T) {
	ts := newTestServer(t)
	bind := mapiPost(t, ts, "/mapi/nspi", "Bind", bindBody(0), nil)
	bind.Body.Close()
	sid, seq := cookieByName(bind, "sid"), cookieByName(bind, "sequence")
	if sid == "" || seq == "" {
		t.Fatal("no cookies from Bind")
	}
	var body []byte
	body = binary.LittleEndian.AppendUint32(body, 0) // flags
	body = append(body, 1)                           // hasStat
	body = append(body, statBytes(0x10)...)          // cur_rec = midBase (first entry)
	body = append(body, 0)                           // hasTags = 0 (default bag)
	body = binary.LittleEndian.AppendUint32(body, 0) // cb_auxin
	gp := mapiPost(t, ts, "/mapi/nspi", "GetProps", body, func(r *http.Request) {
		r.AddCookie(&http.Cookie{Name: "sid", Value: sid})
		r.AddCookie(&http.Cookie{Name: "sequence", Value: seq})
	})
	defer gp.Body.Close()
	if got := gp.Header.Get("X-ResponseCode"); got != "0" {
		t.Fatalf("GetProps: X-ResponseCode = %q, want 0", got)
	}
	p := nspiPayload(t, gp)
	// status(0:4) + result(4:8) + codepage(8:12) + row-marker(12)
	if len(p) < 13 {
		t.Fatalf("response too short: %d bytes", len(p))
	}
	if result := binary.LittleEndian.Uint32(p[4:]); result != 0 {
		t.Errorf("result = %#x, want 0 (ecSuccess)", result)
	}
	if p[12] != 0xFF {
		t.Errorf("row marker = %#x, want 0xFF (a row follows)", p[12])
	}
	if u16 := []byte{'a', 0, 'l', 0, 'i', 0, 'c', 0, 'e', 0}; !bytes.Contains(p, u16) {
		t.Error("GetProps row does not carry the entry as UTF-16LE")
	}
}

// TestNspiGetPropList drives Bind then GetPropList for the first entry MId and
// confirms the route returns a property-tag list.
func TestNspiGetPropList(t *testing.T) {
	ts := newTestServer(t)
	bind := mapiPost(t, ts, "/mapi/nspi", "Bind", bindBody(0), nil)
	bind.Body.Close()
	sid, seq := cookieByName(bind, "sid"), cookieByName(bind, "sequence")
	if sid == "" || seq == "" {
		t.Fatal("no cookies from Bind")
	}
	var body []byte
	body = binary.LittleEndian.AppendUint32(body, 0)    // flags
	body = binary.LittleEndian.AppendUint32(body, 0x10) // MId = midBase
	body = binary.LittleEndian.AppendUint32(body, 1252) // code page
	body = binary.LittleEndian.AppendUint32(body, 0)    // cb_auxin
	gpl := mapiPost(t, ts, "/mapi/nspi", "GetPropList", body, func(r *http.Request) {
		r.AddCookie(&http.Cookie{Name: "sid", Value: sid})
		r.AddCookie(&http.Cookie{Name: "sequence", Value: seq})
	})
	defer gpl.Body.Close()
	if got := gpl.Header.Get("X-ResponseCode"); got != "0" {
		t.Fatalf("GetPropList: X-ResponseCode = %q, want 0", got)
	}
	p := nspiPayload(t, gpl)
	// status(0:4) + result(4:8) + tags-marker(8) + count(9:13)
	if len(p) < 13 {
		t.Fatalf("response too short: %d bytes", len(p))
	}
	if result := binary.LittleEndian.Uint32(p[4:]); result != 0 {
		t.Errorf("result = %#x, want 0", result)
	}
	if p[8] != 0xFF {
		t.Errorf("tags marker = %#x, want 0xFF (a tag list follows)", p[8])
	}
}
