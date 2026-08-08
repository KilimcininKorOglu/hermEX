package webmail2api

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"hermex/internal/antivirus"
	"hermex/internal/avtest"
	"hermex/internal/directory"
	"hermex/internal/mapi"
	"hermex/internal/mta"
	"hermex/internal/objectstore"
)

// scanDir is the directory capability the scanner gates on: one domain with
// inbound scanning enabled.
type scanDir struct{}

func (scanDir) GetDomainAVScan(string) (bool, bool, error)   { return true, false, nil }
func (scanDir) DomainID(string) (int64, bool, error)         { return 7, true, nil }
func (scanDir) DomainOrgAdminEmails(int64) ([]string, error) { return nil, nil }
func (scanDir) QuarantineMessage(directory.QuarantineEntry) (int64, error) {
	return 1, nil
}

// withScanner points the package-level scanner at a stub clamd for one test.
func withScanner(t *testing.T, verdict string) {
	t.Helper()
	sc, err := antivirus.New(avtest.Clamd(t, verdict))
	if err != nil {
		t.Fatal(err)
	}
	quar := t.TempDir()
	mta.SetScanner(sc, scanDir{}, func(int64) string { return quar + "/q.eml" }, "mail.hermex.test", nil)
	t.Cleanup(func() { mta.SetScanner(nil, nil, nil, "", nil) })
}

// importEML posts an .eml to the import endpoint and returns the status and body.
func importEML(t *testing.T, srv *Server, token, raw string) (int, string) {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"file": base64.StdEncoding.EncodeToString([]byte(raw))})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/mail/import", strings.NewReader(string(body)))
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

// importHarness builds a server with one account and a session for it.
func importHarness(t *testing.T) (*Server, string, string) {
	t.Helper()
	mbox := t.TempDir()
	secret := []byte("import-scan-secret")
	accounts := directory.StaticAccounts{"alice@hermex.test": {Password: "pw", MailboxPath: mbox}}
	srv := NewServer(accounts, accounts, nil, "mail.hermex.test", secret, "", false)
	token, err := mintToken(secret, sessionClaims{
		Email: "alice@hermex.test", Mailbox: mbox, Exp: time.Now().Add(time.Hour).Unix(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return srv, token, mbox
}

// inboxSubjects returns the subject of every message in the mailbox's inbox.
func inboxSubjects(t *testing.T, mbox string) []string {
	t.Helper()
	st, err := objectstore.Open(mbox)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	msgs, err := st.ListMessages(int64(mapi.PrivateFIDInbox))
	if err != nil {
		t.Fatal(err)
	}
	out := make([]string, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, m.Subject)
	}
	return out
}

// TestImportRefusesInfectedContent proves an uploaded .eml is scanned. An import
// never passes through delivery, so without the scan it is a way to place malware
// straight into a mailbox.
func TestImportRefusesInfectedContent(t *testing.T) {
	withScanner(t, avtest.Infected)
	srv, token, mbox := importHarness(t)

	code, body := importEML(t, srv, token, "Subject: infected\r\n\r\nMZ malware bytes\r\n")
	if code == http.StatusOK {
		t.Errorf("infected import was accepted: %s", body)
	}
	// The uploaded message is not in the mailbox. What is there is the quarantine
	// notice, which is the point: the user is told why the import was refused.
	for _, subject := range inboxSubjects(t, mbox) {
		if strings.Contains(subject, "infected") {
			t.Errorf("the infected message was stored anyway (subject %q)", subject)
		}
	}
}

// TestImportAcceptsCleanContent keeps the ordinary import working.
func TestImportAcceptsCleanContent(t *testing.T) {
	withScanner(t, avtest.Clean)
	srv, token, mbox := importHarness(t)

	code, body := importEML(t, srv, token, "Subject: fine\r\n\r\nbody\r\n")
	if code != http.StatusOK {
		t.Fatalf("clean import status = %d: %s", code, body)
	}
	subjects := inboxSubjects(t, mbox)
	if len(subjects) != 1 || !strings.Contains(subjects[0], "fine") {
		t.Errorf("the imported message is not in the mailbox: %v", subjects)
	}
}
