package mapihttp

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"hermex/internal/directory"
)

// tenantAccounts answers the address book with only the entries in the caller's
// own domain, the shape a real directory applies. It stands in for the SQL
// directory so this package can prove the transport hands the authenticated
// identity down to the address book rather than reproving the SQL scope.
type tenantAccounts struct {
	directory.StaticAccounts
}

func (a tenantAccounts) SearchGAL(caller, _ string, _ int) ([]directory.GALEntry, error) {
	dom := afterAt(caller)
	if dom == "" {
		return nil, nil
	}
	var out []directory.GALEntry
	for addr, acc := range a.StaticAccounts {
		if afterAt(addr) == dom {
			out = append(out, directory.GALEntry{DisplayName: addr, Address: addr, StorePath: acc.MailboxPath})
		}
	}
	return out, nil
}

// afterAt returns the part of an address after the '@', or "" when there is none.
func afterAt(addr string) string {
	if i := bytes.LastIndexByte([]byte(addr), '@'); i >= 0 {
		return addr[i+1:]
	}
	return ""
}

// wireName spells an ASCII string the way the address-book wire carries it, so a
// response body can be searched for a name without decoding it.
func wireName(s string) []byte {
	var b []byte
	for _, c := range []byte(s) {
		b = append(b, c, 0)
	}
	return b
}

// TestNSPIQueryRowsIsScopedToTheAuthenticatedUser proves the leak is closed on
// the transport Outlook actually uses. The address book was built once for the
// whole server, so an Outlook client signed into one tenant browsed every mailbox
// the server hosted, including every other tenant's.
func TestNSPIQueryRowsIsScopedToTheAuthenticatedUser(t *testing.T) {
	const neighbour = "eve@other.test"
	accs := tenantAccounts{directory.StaticAccounts{
		testUser:  {Password: testPass, MailboxPath: t.TempDir()},
		neighbour: {Password: testPass, MailboxPath: t.TempDir()},
	}}
	ts := httptest.NewServer(NewServer(accs, accs, "mail.hermex.test", nil).Handler())
	t.Cleanup(ts.Close)

	sid, seq := boundSession(t, ts)
	qr := mapiPost(t, ts, "/mapi/nspi", "QueryRows", queryRowsBody(), withSession(sid, seq))
	defer qr.Body.Close()
	if got := qr.Header.Get("X-ResponseCode"); got != "0" {
		t.Fatalf("QueryRows: X-ResponseCode = %q, want 0", got)
	}
	p := nspiPayload(t, qr)

	if !bytes.Contains(p, wireName("alice")) {
		t.Error("the caller's own tenant is missing from the address book")
	}
	if bytes.Contains(p, wireName("eve")) {
		t.Error("the address book carries another tenant's mailbox")
	}
}

// TestNSPIQueryRowsRefusesAnUnauthenticatedBrowse is the fail-closed guard: the
// endpoint requires Basic auth, so an unauthenticated browse must never reach the
// address book at all.
func TestNSPIQueryRowsRefusesAnUnauthenticatedBrowse(t *testing.T) {
	ts := newTestServer(t)
	sid, seq := boundSession(t, ts)

	qr := mapiPost(t, ts, "/mapi/nspi", "QueryRows", queryRowsBody(), func(r *http.Request) {
		r.Header.Del("Authorization")
		withSession(sid, seq)(r)
	})
	defer qr.Body.Close()
	if qr.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", qr.StatusCode)
	}
}
