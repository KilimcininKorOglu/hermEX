package webmail2api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"hermex/internal/directory"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// sentCopyHarness signs a mailbox owner in and returns a browser for the endpoint.
func sentCopyHarness(t *testing.T) requestFunc {
	t.Helper()
	dir := t.TempDir()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	mustClose(t, st)
	accounts := directory.StaticAccounts{
		"alice@hermex.test": {Password: "pw", MailboxPath: dir},
	}
	return loginAs(t, accounts, "alice@hermex.test")
}

// loginAs signs user (password "pw") in and returns a browser that keeps the
// session cookie.
func loginAs(t *testing.T, accounts directory.StaticAccounts, user string) requestFunc {
	t.Helper()
	srv := NewServer(accounts, accounts, nil, "mail.hermex.test", []byte("sent-copy-secret"), "", false)
	var jar []*http.Cookie
	do := requestFunc(func(method, target, body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		var req *http.Request
		if body == "" {
			req = httptest.NewRequest(method, target, nil)
		} else {
			req = httptest.NewRequest(method, target, strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
		}
		for _, c := range jar {
			req.AddCookie(c)
		}
		srv.Handler().ServeHTTP(rec, req)
		if set := rec.Result().Cookies(); len(set) > 0 {
			jar = set
		}
		return rec
	})
	if rec := do(http.MethodPost, "/api/v1/auth/login",
		`{"email":"`+user+`","password":"pw"}`); rec.Code != http.StatusOK {
		t.Fatalf("login = %d", rec.Code)
	}
	return do
}

// readSentCopy decodes the sent-copy endpoint's answer.
func readSentCopy(t *testing.T, rec *httptest.ResponseRecorder) sentCopyJSON {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var out sentCopyJSON
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// TestSentCopySettingRoundTrips proves the mailbox owner can read and change both
// flags, and that a PUT carrying the whole object never drops the flag it did not
// change.
func TestSentCopySettingRoundTrips(t *testing.T) {
	do := sentCopyHarness(t)

	if got := readSentCopy(t, do(http.MethodGet, "/api/v1/account/sent-copy", "")); got != (sentCopyJSON{}) {
		t.Errorf("an unconfigured mailbox reads as %+v, want all off", got)
	}

	for _, want := range []sentCopyJSON{
		{ForSendAs: true},
		{ForSendAs: true, ForSendOnBehalf: true},
		{ForSendOnBehalf: true},
		{ForSendAs: true, Exclusive: true},
		{},
	} {
		body := mustEncode(t, want)
		if got := readSentCopy(t, do(http.MethodPut, "/api/v1/account/sent-copy", string(body))); got != want {
			t.Errorf("PUT echoed %+v, want %+v", got, want)
		}
		if got := readSentCopy(t, do(http.MethodGet, "/api/v1/account/sent-copy", "")); got != want {
			t.Errorf("GET after PUT %+v read %+v", want, got)
		}
	}
}

// TestSentCopyNeedsASession keeps the setting the mailbox owner's: it decides where
// copies of mail sent in their name land, so an unauthenticated caller gets nothing.
func TestSentCopyNeedsASession(t *testing.T) {
	dir := t.TempDir()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	mustClose(t, st)
	accounts := directory.StaticAccounts{"alice@hermex.test": {Password: "pw", MailboxPath: dir}}
	srv := NewServer(accounts, accounts, nil, "mail.hermex.test", []byte("sent-copy-secret"), "", false)

	for _, c := range []struct{ method, body string }{
		{http.MethodGet, ""},
		{http.MethodPut, `{"forSendAs":true}`},
	} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(c.method, "/api/v1/account/sent-copy", strings.NewReader(c.body))
		req.Header.Set("Content-Type", "application/json")
		srv.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s without a session = %d, want 401", c.method, rec.Code)
		}
	}
	st2 := openMailbox(t, dir)
	cfg, err := st2.GetSentCopyConfig()
	mustNoErr(t, "read sent-copy config", err)
	if cfg != (objectstore.SentCopyConfig{}) {
		t.Errorf("the unauthenticated PUT changed the setting to %+v", cfg)
	}
}

// TestSentCopyRecordsAStoreFailure: when the mailbox cannot be opened, read
// and write answer 500 and record why.
func TestSentCopyRecordsAStoreFailure(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "never-provisioned")
	do := loginAs(t, directory.StaticAccounts{"alice@hermex.test": {Password: "pw", MailboxPath: missing}}, "alice@hermex.test")
	for _, c := range []struct{ method, body, op string }{
		{http.MethodGet, "", "sent-copy-read"},
		{http.MethodPut, `{"forSendAs":true}`, "sent-copy-write"},
	} {
		sink := withSink(t)
		wantStatus(t, c.method, do(c.method, "/api/v1/account/sent-copy", c.body), http.StatusInternalServerError)
		if _, ok := sink.find(c.op); !ok {
			t.Errorf("%s: the store failure left no %s record", c.method, c.op)
		}
	}
}

// TestSentCopyRefusesAMalformedBody leaves the stored setting as it was.
func TestSentCopyRefusesAMalformedBody(t *testing.T) {
	do := sentCopyHarness(t)
	want := sentCopyJSON{ForSendAs: true}
	readSentCopy(t, do(http.MethodPut, "/api/v1/account/sent-copy", string(mustEncode(t, want))))
	for _, body := range []string{`{`, `{"forSendAs":"yes"}`, `[]`} {
		if rec := do(http.MethodPut, "/api/v1/account/sent-copy", body); rec.Code == http.StatusOK {
			t.Errorf("PUT %s answered 200", body)
		}
	}
	if got := readSentCopy(t, do(http.MethodGet, "/api/v1/account/sent-copy", "")); got != want {
		t.Errorf("after refused PUTs the setting reads %+v, want %+v", got, want)
	}
}

// exclusiveSharedMailbox provisions alice and a shared mailbox that grants her
// send-as, files its own copy and asks for it to be the only one.
func exclusiveSharedMailbox(t *testing.T) (accounts directory.StaticAccounts, aliceDir, sharedDir string) {
	t.Helper()
	aliceDir, sharedDir = t.TempDir(), t.TempDir()
	ast, err := objectstore.Open(aliceDir)
	mustNoErr(t, "open alice's mailbox", err)
	mustClose(t, ast)
	shared, err := objectstore.Open(sharedDir)
	if err != nil {
		t.Fatal(err)
	}
	defer mustClose(t, shared)
	if err := shared.SetSendAs([]string{"alice@hermex.test"}); err != nil {
		t.Fatal(err)
	}
	if err := shared.SetSentCopyConfig(objectstore.SentCopyConfig{ForSendAs: true, Exclusive: true}); err != nil {
		t.Fatal(err)
	}
	return directory.StaticAccounts{
		"alice@hermex.test":  {Password: "pw", MailboxPath: aliceDir},
		"shared@hermex.test": {Shared: true, MailboxPath: sharedDir},
	}, aliceDir, sharedDir
}

// sentItems counts the messages in a mailbox's Sent Items.
func sentItems(t *testing.T, dir string) int {
	t.Helper()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer mustClose(t, st)
	msgs, err := st.ListMessages(int64(mapi.PrivateFIDSentItems))
	if err != nil {
		t.Fatal(err)
	}
	return len(msgs)
}

// TestSendLeavesTheOnlyCopyToAnExclusiveMailbox sends as a shared mailbox that files
// its own copy and asks for it to be the only one: alice's Sent Items stays empty.
func TestSendLeavesTheOnlyCopyToAnExclusiveMailbox(t *testing.T) {
	accounts, aliceDir, sharedDir := exclusiveSharedMailbox(t)
	do := loginAs(t, accounts, "alice@hermex.test")
	if rec := do(http.MethodPost, "/api/v1/mail/send",
		`{"from":"shared@hermex.test","to":["shared@hermex.test"],"subject":"team","body":"hi"}`); rec.Code != http.StatusOK {
		t.Fatalf("send = %d: %s", rec.Code, rec.Body.String())
	}
	if n := sentItems(t, aliceDir); n != 0 {
		t.Errorf("alice's Sent Items holds %d, want none", n)
	}
	if n := sentItems(t, sharedDir); n != 1 {
		t.Errorf("the shared mailbox's Sent Items holds %d, want its copy", n)
	}
}
