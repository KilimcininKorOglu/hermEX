package webmail2api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"hermex/internal/directory"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// pagedHarness builds a server whose mailbox holds n inbox messages, half of them
// already read, and returns a request function for the listing endpoint.
func pagedHarness(t *testing.T, n int) func(query string) map[string]any {
	t.Helper()
	mbox := t.TempDir()
	st, err := objectstore.Open(mbox)
	if err != nil {
		t.Fatal(err)
	}
	for i := range n {
		raw := fmt.Sprintf("From: s%03d@hermex.test\r\nSubject: subject %03d\r\n\r\nbody", i, i)
		var flags int64
		if i%2 == 0 {
			flags |= objectstore.FlagSeen
		}
		if _, err := st.AppendMessage(int64(mapi.PrivateFIDInbox), []byte(raw), time.Unix(1700000000+int64(i)*60, 0), flags); err != nil {
			st.Close()
			t.Fatal(err)
		}
	}
	st.Close()

	secret := []byte("mail-page-test-secret")
	accounts := directory.StaticAccounts{"alice@hermex.test": {Password: "pw", MailboxPath: mbox}}
	srv := NewServer(accounts, accounts, nil, "mail.hermex.test", secret, "", false)
	token, err := mintToken(secret, sessionClaims{
		Email: "alice@hermex.test", Mailbox: mbox, Exp: time.Now().Add(time.Hour).Unix(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return func(query string) map[string]any {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/api/v1/mail/inbox"+query, nil)
		req.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s = %d: %s", query, rec.Code, rec.Body.String())
		}
		var out map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("response is not JSON: %v (%s)", err, rec.Body.String())
		}
		return out
	}
}

// emails returns the listing rows of one response.
func emailsOf(t *testing.T, body map[string]any) []map[string]any {
	t.Helper()
	raw, ok := body["emails"].([]any)
	if !ok {
		t.Fatalf("response carries no emails array: %v", body)
	}
	out := make([]map[string]any, 0, len(raw))
	for _, e := range raw {
		m, ok := e.(map[string]any)
		if !ok {
			t.Fatalf("email row is not an object: %v", e)
		}
		out = append(out, m)
	}
	return out
}

// TestFolderPageContract proves the endpoint still answers with one page plus the
// whole-folder counts after the pagination moved into the query.
func TestFolderPageContract(t *testing.T) {
	get := pagedHarness(t, 25)

	body := get("?pageSize=10&page=1")
	if got := len(emailsOf(t, body)); got != 10 {
		t.Errorf("page holds %d rows, want 10", got)
	}
	if total, _ := body["total"].(float64); total != 25 {
		t.Errorf("total = %v, want 25", body["total"])
	}
	if unread, _ := body["unread"].(float64); unread != 12 {
		t.Errorf("unread = %v, want 12", body["unread"])
	}

	// The pages tile the folder: page 2 continues where page 1 stopped.
	first := emailsOf(t, get("?pageSize=10&page=0"))
	second := emailsOf(t, get("?pageSize=10&page=1"))
	if first[0]["id"] == second[0]["id"] {
		t.Error("page 1 repeats page 0")
	}
	last := emailsOf(t, get("?pageSize=10&page=2"))
	if len(last) != 5 {
		t.Errorf("last page holds %d rows, want the 5 that remain", len(last))
	}
}

// TestFolderPageFilterAndSort proves the filter and ordering parameters survived
// the move into the query.
func TestFolderPageFilterAndSort(t *testing.T) {
	get := pagedHarness(t, 25)

	unread := get("?filter=unread")
	if total, _ := unread["total"].(float64); total != 12 {
		t.Errorf("unread total = %v, want 12", unread["total"])
	}
	for _, row := range emailsOf(t, unread) {
		if read, _ := row["read"].(bool); read {
			t.Fatalf("a read message came back under filter=unread: %v", row)
		}
	}

	asc := emailsOf(t, get("?sort=subject&dir=asc&pageSize=5&page=0"))
	desc := emailsOf(t, get("?sort=subject&dir=desc&pageSize=5&page=0"))
	if len(asc) != 5 || len(desc) != 5 {
		t.Fatalf("sorted pages hold %d/%d rows, want 5 each", len(asc), len(desc))
	}
	if asc[0]["subject"] == desc[0]["subject"] {
		t.Error("ascending and descending subject ordering returned the same first row")
	}
}

// TestFolderWithoutPageSizeReturnsEverything keeps the opt-in behaviour: a caller
// that asks for no page still gets the whole folder.
func TestFolderWithoutPageSizeReturnsEverything(t *testing.T) {
	get := pagedHarness(t, 25)
	if got := len(emailsOf(t, get(""))); got != 25 {
		t.Errorf("unpaged listing holds %d rows, want 25", got)
	}
}
