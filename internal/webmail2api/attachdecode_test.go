package webmail2api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"hermex/internal/directory"
)

// sendAttachmentServer builds a server with one account, enough to reach the
// message build.
func sendAttachmentServer(t *testing.T) (*Server, string) {
	t.Helper()
	box := t.TempDir()
	accounts := directory.StaticAccounts{"alice@hermex.test": {MailboxPath: box}}
	secret := []byte("attachment-decode-test-secret")
	srv := NewServer(accounts, accounts, nil, "mail.hermex.test", secret, "", false)
	token, err := mintToken(secret, sessionClaims{
		Email: "alice@hermex.test", Mailbox: box, Exp: time.Now().Add(time.Hour).Unix(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return srv, token
}

// postBuild asks for the built message bytes, the path that returns the message
// without delivering it.
func postBuild(t *testing.T, srv *Server, token string, att mailAttachment) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"to":          []string{"bob@example.org"},
		"subject":     "with an attachment",
		"body":        "see attached",
		"attachments": []mailAttachment{att},
	})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/mail/build", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

// TestUndecodableAttachmentFailsTheSend is the silent data-loss defect. An
// attachment whose body will not decode used to be skipped, and the send reported
// success, so the recipient got a message the sender believed carried a file it
// did not, with nothing recorded anywhere.
func TestUndecodableAttachmentFailsTheSend(t *testing.T) {
	srv, token := sendAttachmentServer(t)

	rec := postBuild(t, srv, token, mailAttachment{
		Filename:    "quarterly.pdf",
		ContentType: "application/pdf",
		Content:     "!!!! not base64 !!!!",
	})

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; the attachment was dropped instead of refused", rec.Code, http.StatusBadRequest)
	}
	if !strings.Contains(rec.Body.String(), "quarterly.pdf") {
		t.Errorf("the refusal does not name the file that failed: %s", rec.Body.String())
	}
}

// TestDecodableAttachmentStillBuilds is the control: an ordinary attachment must
// still go through.
func TestDecodableAttachmentStillBuilds(t *testing.T) {
	srv, token := sendAttachmentServer(t)

	rec := postBuild(t, srv, token, mailAttachment{
		Filename:    "note.txt",
		ContentType: "text/plain",
		Content:     "aGVsbG8=", // "hello"
	})

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
}
