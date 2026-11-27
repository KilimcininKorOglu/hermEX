package webmail2api

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"hermex/internal/directory"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// composeHarness builds a server with one account and a session token for it.
func composeHarness(t *testing.T) (*Server, string) {
	t.Helper()
	return composeOver(t, freshMailbox(t))
}

// composeHarnessWithMailbox is composeHarness that also returns the mailbox
// directory, for a test that checks what a refused request left in the store.
func composeHarnessWithMailbox(t *testing.T) (*Server, string, string) {
	t.Helper()
	// Provision the mailbox, as account creation does, so a request that gets
	// past the checks can really write and a refusal is the handler's, not a
	// missing store's.
	mbox := freshMailbox(t)
	srv, token := composeOver(t, mbox)
	return srv, token, mbox
}

// composeOver builds the compose server and alice's token over mbox.
func composeOver(t *testing.T, mbox string) (*Server, string) {
	t.Helper()
	secret := []byte("header-inject-test-secret")
	accounts := directory.StaticAccounts{"alice@hermex.test": {Password: "pw", MailboxPath: mbox}}
	srv := NewServer(accounts, accounts, nil, "mail.hermex.test", secret, "", false)
	token, err := mintToken(secret, sessionClaims{
		Email: "alice@hermex.test", Mailbox: mbox, Exp: time.Now().Add(time.Hour).Unix(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return srv, token
}

// post sends one JSON body to a compose endpoint.
func post(t *testing.T, srv *Server, token, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

// injected is a recipient string that is not an address: everything after the
// line break would become a header of its own once written into the message.
const injected = "attacker@evil.test\r\nBcc: victim@hermex.test"

// TestComposeRejectsUnparseableRecipient proves an entry that is not an address
// is refused rather than carried into the message. mail.Address.String() escapes
// only the local part, so the domain half of such a string lands in the header
// block verbatim.
func TestComposeRejectsUnparseableRecipient(t *testing.T) {
	srv, token, mbox := composeHarnessWithMailbox(t)
	body := mustEncode(t, map[string]any{
		"to": []string{injected}, "subject": "hi", "body": "text",
	})

	for _, path := range []string{"/api/v1/mail/send", "/api/v1/mail/build", "/api/v1/mail/draft"} {
		rec := post(t, srv, token, path, string(body))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s status = %d, want 400: %s", path, rec.Code, rec.Body.String())
		}
	}
	// Send files a copy in Sent Items and draft one in Drafts; a refused request
	// must leave both empty, or the injected header reaches the store anyway.
	st, err := objectstore.Open(mbox)
	mustNoErr(t, "open the mailbox", err)
	defer mustClose(t, st)
	for _, fid := range []int64{int64(mapi.PrivateFIDSentItems), int64(mapi.PrivateFIDDraft)} {
		msgs, err := st.ListMessages(fid)
		mustNoErr(t, "list a folder", err)
		wantEq(t, "messages in folder "+strconv.FormatInt(fid, 10), len(msgs), 0)
	}
}

// TestBuildDoesNotEmitInjectedHeader is the end-to-end proof on the one endpoint
// that returns the built message: no Bcc line appears in what would be sent.
func TestBuildDoesNotEmitInjectedHeader(t *testing.T) {
	srv, token := composeHarness(t)
	body := mustEncode(t, map[string]any{
		"to": []string{"bob@hermex.test", injected}, "subject": "hi", "body": "text",
	})

	wantStatus(t, "build with an injected recipient", post(t, srv, token, "/api/v1/mail/build", string(body)), http.StatusBadRequest)

	// A line break in the subject must not open a header of its own either
	// (RFC 5322 section 2.2: a field body holds no bare CR or LF).
	subj := mustEncode(t, map[string]any{
		"to": []string{"bob@hermex.test"}, "subject": "hi\r\nBcc: victim@hermex.test", "body": "text",
	})
	rec := post(t, srv, token, "/api/v1/mail/build", string(subj))
	if rec.Code != http.StatusOK {
		return
	}
	var got map[string]string
	mustNoErr(t, "decode build", json.Unmarshal(rec.Body.Bytes(), &got))
	raw, err := base64.StdEncoding.DecodeString(got["raw"])
	mustNoErr(t, "decode raw", err)
	for line := range strings.SplitSeq(string(raw), "\r\n") {
		if strings.HasPrefix(line, "Bcc:") {
			t.Errorf("the built message carries an injected header line %q", line)
		}
	}
}

// TestComposeAcceptsOrdinaryAddresses keeps the shapes real clients send working,
// including a display name and a non-ASCII address.
func TestComposeAcceptsOrdinaryAddresses(t *testing.T) {
	srv, token := composeHarness(t)
	body := mustEncode(t, map[string]any{
		"to":      []string{"Bob Smith <bob@hermex.test>", "posta@örnek.test", "carol@hermex.test"},
		"subject": "hi", "body": "text",
	})

	rec := post(t, srv, token, "/api/v1/mail/build", string(body))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
}
