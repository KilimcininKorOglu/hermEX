package webmail2api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"hermex/internal/directory"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// TestHandleAssignTask proves assigning a task stamps the assignment spine (Owner,
// Assigner, AcceptanceState=unknown) and delivers an IPM.TaskRequest message to the
// assignee's Inbox, so the task owner and the assignee each see the assignment.
func TestHandleAssignTask(t *testing.T) {
	root := t.TempDir()
	alicePath := filepath.Join(root, "alice")
	bobPath := filepath.Join(root, "bob")
	for _, p := range []string{alicePath, bobPath} {
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
		// Initialize each store so mta.Deliver can open it.
		st, err := objectstore.Open(p)
		if err != nil {
			t.Fatal(err)
		}
		st.Close()
	}
	accounts := directory.StaticAccounts{
		"alice@hermex.test": {Password: "$6$rounds=5000$x$x", MailboxPath: alicePath},
		"bob@hermex.test":   {Password: "$6$rounds=5000$x$x", MailboxPath: bobPath},
	}
	secret := []byte("assign-test-secret")
	srv := NewServer(accounts, accounts, nil, "mail.hermex.test", secret, "", false)

	mkToken := func(email, mbox string) string {
		tok, _ := mintToken(secret, sessionClaims{Email: email, Mailbox: mbox, Exp: time.Now().Add(time.Hour).Unix()})
		return tok
	}
	do := func(method, target, body string) *httptest.ResponseRecorder {
		var req *http.Request
		if body == "" {
			req = httptest.NewRequest(method, target, nil)
		} else {
			req = httptest.NewRequest(method, target, strings.NewReader(body))
		}
		req.AddCookie(&http.Cookie{Name: sessionCookie, Value: mkToken("alice@hermex.test", alicePath)})
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		return rec
	}

	// Alice creates a task.
	create := do(http.MethodPost, "/api/v1/tasks", `{"summary":"Review PR","description":"please review","completed":false}`)
	if create.Code != http.StatusOK {
		t.Fatalf("create: %d %s", create.Code, create.Body.String())
	}
	var created taskJSON
	if err := json.Unmarshal(create.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}

	// Alice assigns it to bob.
	assign := do(http.MethodPost, "/api/v1/tasks/"+created.UID+"/assign", `{"assignee":"bob@hermex.test"}`)
	if assign.Code != http.StatusOK {
		t.Fatalf("assign: %d %s", assign.Code, assign.Body.String())
	}
	var res struct {
		Owner       string `json:"owner"`
		Assigner    string `json:"assigner"`
		AcceptState int    `json:"acceptState"`
	}
	if err := json.Unmarshal(assign.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	if res.Owner != "bob@hermex.test" {
		t.Errorf("owner = %q, want bob@hermex.test", res.Owner)
	}
	if res.Assigner != "alice@hermex.test" {
		t.Errorf("assigner = %q, want alice@hermex.test", res.Assigner)
	}
	if res.AcceptState != 1 {
		t.Errorf("acceptState = %d, want 1 (unknown)", res.AcceptState)
	}

	// Bob's Inbox carries the assignment message.
	bob, err := objectstore.Open(bobPath)
	if err != nil {
		t.Fatal(err)
	}
	defer bob.Close()
	msgs, err := bob.ListMessages(int64(mapi.PrivateFIDInbox))
	if err != nil {
		t.Fatalf("list bob inbox: %v", err)
	}
	if len(msgs) == 0 {
		t.Fatal("bob's inbox has no assignment message")
	}
}
