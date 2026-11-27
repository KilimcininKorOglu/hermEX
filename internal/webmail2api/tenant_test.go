package webmail2api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"hermex/internal/directory"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// TestFreeBusyStaysInsideTheOrganization proves the scheduling grid reads
// another user's busy time only within the caller's organization: bob and dave
// both granted alice free/busy by name, and only bob, in her domain, answers.
func TestFreeBusyStaysInsideTheOrganization(t *testing.T) {
	day := time.Date(2026, 6, 19, 9, 0, 0, 0, time.UTC)
	accs := directory.StaticAccounts{}
	for _, addr := range []string{"alice@hermex.test", "bob@hermex.test", "dave@other.test"} {
		dir := t.TempDir()
		st, err := objectstore.Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		if err := st.ModifyPermissions(int64(mapi.PrivateFIDCalendar), false, []objectstore.PermissionChange{
			{Op: objectstore.PermAdd, Username: "alice@hermex.test", Rights: mapi.FrightsFreeBusySimple},
		}); err != nil {
			t.Fatal(err)
		}
		mustClose(t, st)
		seedBusyBlock(t, dir, day, day.Add(time.Hour), 2)
		accs[addr] = directory.Account{MailboxPath: dir}
	}
	secret := []byte("tenant-test-secret")
	srv := NewServer(accs, accs, nil, "mail.hermex.test", secret, "", false)
	token, err := mintToken(secret, sessionClaims{Email: "alice@hermex.test", Mailbox: accs["alice@hermex.test"].MailboxPath, Exp: time.Now().Add(time.Hour).Unix()})
	if err != nil {
		t.Fatal(err)
	}
	q := url.Values{
		"users": {"bob@hermex.test,dave@other.test,ghost@hermex.test"},
		"start": {day.Add(-time.Hour).Format(time.RFC3339)},
		"end":   {day.Add(8 * time.Hour).Format(time.RFC3339)},
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/calendar/freebusy?"+q.Encode(), nil)
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	var out struct {
		FreeBusy []struct {
			User string            `json:"user"`
			Busy []json.RawMessage `json:"busy"`
		} `json:"freeBusy"`
	}
	wantStatus(t, "freebusy", rec, http.StatusOK)
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("freebusy = %d: %s", rec.Code, rec.Body.String())
	}
	got := map[string]int{}
	for _, e := range out.FreeBusy {
		got[e.User] = len(e.Busy)
	}
	if got["bob@hermex.test"] != 1 || got["dave@other.test"] != 0 || got["ghost@hermex.test"] != 0 {
		t.Errorf("busy blocks = %v, want bob 1; dave (another organization) and ghost (unknown) 0", got)
	}
}
