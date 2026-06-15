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
