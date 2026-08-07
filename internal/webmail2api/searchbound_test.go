package webmail2api

import (
	"context"
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

// searchResponse is the shape handleSearch answers with.
type searchResponse struct {
	Emails    []mailJSON `json:"emails"`
	Total     int        `json:"total"`
	Truncated bool       `json:"truncated"`
}

// searchTestServer builds a server over one real mailbox and returns it with the
// session secret and the mailbox path, so a test can seed messages directly.
func searchTestServer(t *testing.T) (*Server, []byte, string) {
	t.Helper()
	secret := []byte("search-bound-test-secret")
	mailbox := t.TempDir()
	accs := directory.StaticAccounts{"alice@hermex.test": {Password: "pw", MailboxPath: mailbox}}
	srv := NewServer(accs, accs, nil, "mail.hermex.test", secret, "", false)
	return srv, secret, mailbox
}

// seedInbox files n messages in the Inbox, each one matching a body search for
// "needle" so every one of them costs a read and a parse.
func seedInbox(t *testing.T, mailbox string, n int) {
	t.Helper()
	st, err := objectstore.Open(mailbox)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	for i := range n {
		raw := fmt.Appendf(nil,
			"From: sender%d@hermex.test\r\nTo: alice@hermex.test\r\nSubject: message %d\r\n"+
				"Date: Wed, 15 Nov 2023 10:13:20 +0000\r\n\r\nneedle in the body %d\r\n", i, i, i)
		if _, err := st.AppendMessage(mapi.PrivateFIDInbox, raw, time.Unix(1700000000+int64(i), 0), 0); err != nil {
			t.Fatal(err)
		}
	}
}

// runSearch issues an authenticated search and decodes the reply.
func runSearch(t *testing.T, srv *Server, secret []byte, mailbox, query string) searchResponse {
	t.Helper()
	rec := authedGetAs(t, srv, secret, "alice@hermex.test", mailbox, "/api/v1/search?q="+query)
	if rec.Code != 200 {
		t.Fatalf("search status %d: %s", rec.Code, rec.Body.String())
	}
	var out searchResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode search reply: %v", err)
	}
	return out
}

// TestSearchCapsItsAnswer proves a broad query no longer returns everything it
// finds. Search has no index behind it: a body filter reads and parses each
// message, so an unbounded walk turned one interactive request into a disk and
// CPU burst across the whole mailbox and an arbitrarily large JSON array.
func TestSearchCapsItsAnswer(t *testing.T) {
	srv, secret, mailbox := searchTestServer(t)
	seedInbox(t, mailbox, maxSearchResults+40)

	got := runSearch(t, srv, secret, mailbox, "needle")

	if len(got.Emails) != maxSearchResults {
		t.Errorf("returned %d results, want the cap of %d", len(got.Emails), maxSearchResults)
	}
	if got.Total != len(got.Emails) {
		t.Errorf("total = %d, want the %d actually returned", got.Total, len(got.Emails))
	}
	if !got.Truncated {
		t.Error("the reply presents a partial answer as a whole one")
	}
}

// TestSearchKeepsTheNewestMatches proves the cap keeps the right end of the
// mailbox. The folder listing is ordered oldest first, so a walk that stopped at
// the cap without reversing would answer a search with the oldest mail in the
// account, which is never what was wanted.
func TestSearchKeepsTheNewestMatches(t *testing.T) {
	srv, secret, mailbox := searchTestServer(t)
	const n = maxSearchResults + 40
	seedInbox(t, mailbox, n)

	got := runSearch(t, srv, secret, mailbox, "needle")

	// The newest message is "message n-1"; the oldest is "message 0".
	newest := fmt.Sprintf("message %d", n-1)
	oldest := "message 0"
	var sawNewest, sawOldest bool
	for _, e := range got.Emails {
		switch e.Subject {
		case newest:
			sawNewest = true
		case oldest:
			sawOldest = true
		}
	}
	if !sawNewest {
		t.Errorf("the newest message is missing from a capped search")
	}
	if sawOldest {
		t.Errorf("a capped search kept the oldest message instead")
	}
}

// TestSearchUnderTheCapIsWhole proves the ordinary case is untouched: a query
// that fits reports every match and does not claim to be partial.
func TestSearchUnderTheCapIsWhole(t *testing.T) {
	srv, secret, mailbox := searchTestServer(t)
	seedInbox(t, mailbox, 5)

	got := runSearch(t, srv, secret, mailbox, "needle")

	if len(got.Emails) != 5 {
		t.Errorf("returned %d results, want all 5", len(got.Emails))
	}
	if got.Truncated {
		t.Error("a complete answer is reported as truncated")
	}
}

// TestSearchStopsWhenTheClientGoesAway proves an abandoned request stops the
// walk. Nothing else bounds the handler's runtime, so without it a client that
// navigated away left a full-mailbox read and parse running to completion.
func TestSearchStopsWhenTheClientGoesAway(t *testing.T) {
	srv, secret, mailbox := searchTestServer(t)
	seedInbox(t, mailbox, 50)

	token, err := mintToken(secret, sessionClaims{
		Email: "alice@hermex.test", Mailbox: mailbox, Exp: time.Now().Add(time.Hour).Unix(),
	})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("GET", "/api/v1/search?q=needle", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
	ctx, cancel := context.WithCancel(req.Context())
	cancel() // the client is already gone when the handler starts
	req = req.WithContext(ctx)

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	var out searchResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out.Emails) != 0 || !out.Truncated {
		t.Errorf("an abandoned search returned %d results (truncated=%v); it should stop at once",
			len(out.Emails), out.Truncated)
	}
}
