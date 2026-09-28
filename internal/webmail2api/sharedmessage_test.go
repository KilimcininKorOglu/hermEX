package webmail2api

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"hermex/internal/directory"
	"hermex/internal/mapi"
	"hermex/internal/mime"
	"hermex/internal/objectstore"
	"hermex/internal/oxcical"
)

// sharedMessageFixture is a caller with a mailbox of their own and a shared
// mailbox "team" whose Inbox holds one message at the same uid as the caller's own
// first message, so an operation that lands in the wrong store is visible.
type sharedMessageFixture struct {
	srv           *Server
	token         string
	own, shared   string
	sharedSubject string
}

func newSharedMessageFixture(t *testing.T, rights uint32, delegate bool) *sharedMessageFixture {
	t.Helper()
	f := &sharedMessageFixture{own: t.TempDir(), shared: t.TempDir(), sharedSubject: "team message"}
	appendInbox(t, f.own, "own message")
	appendInbox(t, f.shared, f.sharedSubject)
	st, err := objectstore.Open(f.shared)
	mustNoErr(t, "open shared", err)
	defer st.Close()
	if rights != 0 {
		mustNoErr(t, "grant", st.ModifyPermissions(int64(mapi.PrivateFIDInbox), false, []objectstore.PermissionChange{
			{Op: objectstore.PermAdd, Username: "alice@hermex.test", Rights: rights},
		}))
	}
	if delegate {
		mustNoErr(t, "delegate", st.SetDelegates([]string{"alice@hermex.test"}))
	}
	accounts := directory.StaticAccounts{
		"alice@hermex.test": {Password: "pw", MailboxPath: f.own},
		"team@hermex.test":  {Shared: true, MailboxPath: f.shared},
	}
	secret := []byte("shared-message-test-secret")
	f.srv = NewServer(accounts, accounts, nil, "mail.hermex.test", secret, "", false)
	f.token, err = mintToken(secret, sessionClaims{Email: "alice@hermex.test", Mailbox: f.own, Exp: time.Now().Add(time.Hour).Unix()})
	mustNoErr(t, "token", err)
	return f
}

func appendInbox(t *testing.T, path, subject string) {
	t.Helper()
	st, err := objectstore.Open(path)
	mustNoErr(t, "open", err)
	defer st.Close()
	raw := "From: s@hermex.test\r\nSubject: " + subject + "\r\n\r\nbody"
	_, err = st.AppendMessage(int64(mapi.PrivateFIDInbox), []byte(raw), time.Now(), 0)
	mustNoErr(t, "append", err)
}

func (f *sharedMessageFixture) do(method, target, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: f.token})
	rec := httptest.NewRecorder()
	f.srv.Handler().ServeHTTP(rec, req)
	return rec
}

func inboxCount(t *testing.T, path string) int {
	t.Helper()
	st, err := objectstore.Open(path)
	mustNoErr(t, "open", err)
	defer st.Close()
	msgs, err := st.ListMessages(int64(mapi.PrivateFIDInbox))
	mustNoErr(t, "list", err)
	return len(msgs)
}

// TestSharedMessageReadsTheSharedMailbox proves a per-message read names the
// shared mailbox the request carries in ?owner. It used to open the caller's own
// mailbox and serve the caller's message at the same uid.
func TestSharedMessageReadsTheSharedMailbox(t *testing.T) {
	f := newSharedMessageFixture(t, mapi.RightsReviewer, false)
	rec := f.do(http.MethodGet, "/api/v1/mail/source?id=inbox:1&owner=team@hermex.test", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), f.sharedSubject) {
		t.Errorf("served a message from another mailbox:\n%s", rec.Body.String())
	}
}

// TestSharedMessageDeleteNeedsWrite proves a read-only grantee cannot delete in
// the shared mailbox, and that the refusal leaves the caller's own mailbox alone.
func TestSharedMessageDeleteNeedsWrite(t *testing.T) {
	f := newSharedMessageFixture(t, mapi.RightsReviewer, false)
	rec := f.do(http.MethodDelete, "/api/v1/mail/delete?id=inbox:1&owner=team@hermex.test", "")
	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rec.Code)
	}
	if inboxCount(t, f.shared) != 1 || inboxCount(t, f.own) != 1 {
		t.Error("a refused delete changed a mailbox")
	}
}

// TestSharedMessageDeleteActsOnTheSharedMailbox proves a write grantee deletes
// the shared message and never the caller's message at the same uid.
func TestSharedMessageDeleteActsOnTheSharedMailbox(t *testing.T) {
	f := newSharedMessageFixture(t, mapi.RightsEditor, false)
	// The message leaves the Inbox for Deleted Items, which needs its own grant.
	st, err := objectstore.Open(f.shared)
	mustNoErr(t, "open", err)
	mustNoErr(t, "grant", st.ModifyPermissions(int64(mapi.PrivateFIDDeletedItems), false, []objectstore.PermissionChange{
		{Op: objectstore.PermAdd, Username: "alice@hermex.test", Rights: mapi.RightsEditor},
	}))
	st.Close()
	rec := f.do(http.MethodDelete, "/api/v1/mail/delete?id=inbox:1&owner=team@hermex.test", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if inboxCount(t, f.shared) != 0 {
		t.Error("the shared message is still in the shared Inbox")
	}
	if inboxCount(t, f.own) != 1 {
		t.Error("the delete removed the caller's own message")
	}
}

// TestSharedMessageMoveNeedsTheDestination proves a move is refused when the
// grantee may change the source folder but not the destination.
func TestSharedMessageMoveNeedsTheDestination(t *testing.T) {
	f := newSharedMessageFixture(t, mapi.RightsEditor, false)
	rec := f.do(http.MethodPost, "/api/v1/mail/move?owner=team@hermex.test", `{"id":"inbox:1","to":"junk"}`)
	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403; body=%s", rec.Code, rec.Body.String())
	}
	if inboxCount(t, f.shared) != 1 {
		t.Error("a refused move took the message out of the Inbox")
	}
}

// TestSharedMessageRefusesADelegateWithoutAGrant proves a delegate, who may open
// the mailbox, still reads a message only through a grant on its folder.
func TestSharedMessageRefusesADelegateWithoutAGrant(t *testing.T) {
	f := newSharedMessageFixture(t, 0, true)
	rec := f.do(http.MethodGet, "/api/v1/mail/source?id=inbox:1&owner=team@hermex.test", "")
	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403; body=%s", rec.Code, rec.Body.String())
	}
}

// TestSharedMessageOpenMarksReadWithWriteRights proves opening a shared message
// marks it read for a grantee who may change the folder, and leaves it unread,
// and reported unread, for a read-only grantee.
func TestSharedMessageOpenMarksReadWithWriteRights(t *testing.T) {
	for _, c := range []struct {
		name   string
		rights uint32
		read   bool
	}{
		{"editor", mapi.RightsEditor, true},
		{"reviewer", mapi.RightsReviewer, false},
	} {
		f := newSharedMessageFixture(t, c.rights, false)
		rec := f.do(http.MethodGet, "/api/v1/mail/message?id=inbox:1&owner=team@hermex.test", "")
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status = %d; body=%s", c.name, rec.Code, rec.Body.String())
		}
		wantContains(t, c.name+" detail", rec.Body.String(), `"read":`+map[bool]string{true: "true", false: "false"}[c.read])
		st, err := objectstore.Open(f.shared)
		mustNoErr(t, "open", err)
		msgs, err := st.ListMessages(int64(mapi.PrivateFIDInbox))
		st.Close()
		mustNoErr(t, "list", err)
		wantEq(t, c.name+" stored read state", msgs[0].Flags&objectstore.FlagSeen != 0, c.read)
	}
}

// TestSharedMessageOpenFollowsTheOwnersReceiptSetting proves a delegate who may
// change the folder sends the receipt the owner's "always" setting asks for, in
// the owner's name, and that a read-only delegate sends none and is not asked.
func TestSharedMessageOpenFollowsTheOwnersReceiptSetting(t *testing.T) {
	for _, c := range []struct {
		name   string
		rights uint32
		sent   int
	}{
		{"editor", mapi.RightsEditor, 1},
		{"reviewer", mapi.RightsReviewer, 0},
	} {
		f := newSharedMessageFixture(t, c.rights, false)
		bob := t.TempDir()
		accounts := directory.StaticAccounts{
			"alice@hermex.test": {Password: "pw", MailboxPath: f.own},
			"team@hermex.test":  {Shared: true, MailboxPath: f.shared},
			"bob@hermex.test":   {Password: "pw", MailboxPath: bob},
		}
		f.srv = NewServer(accounts, accounts, nil, "mail.hermex.test", []byte("shared-message-test-secret"), "", false)
		st, err := objectstore.Open(f.shared)
		mustNoErr(t, "open", err)
		mustNoErr(t, "setting", st.SetReadReceiptConfig(objectstore.ReadReceiptConfig{Response: objectstore.ReadReceiptAlways}))
		raw := "Return-Path: <bob@hermex.test>\r\nFrom: bob@hermex.test\r\nTo: team@hermex.test\r\nSubject: rr\r\n" +
			"Disposition-Notification-To: bob@hermex.test\r\n\r\nbody\r\n"
		info, err := st.AppendMessage(int64(mapi.PrivateFIDInbox), []byte(raw), time.Now(), 0)
		st.Close()
		mustNoErr(t, "append", err)
		id := "inbox:" + strconv.FormatUint(uint64(info.UID), 10)
		rec := f.do(http.MethodGet, "/api/v1/mail/message?id="+id+"&owner=team@hermex.test", "")
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status = %d; body=%s", c.name, rec.Code, rec.Body.String())
		}
		if strings.Contains(rec.Body.String(), `"receiptRequested":true`) {
			t.Errorf("%s: the delegate is asked about the owner's receipt", c.name)
		}
		got := folderMail(t, bob, int64(mapi.PrivateFIDInbox))
		wantEq(t, c.name+" receipts", len(got), c.sent)
		if c.sent == 1 {
			wantContains(t, "the receipt", got[0], "Final-Recipient: rfc822;team@hermex.test")
		}
	}
}

// grantFolder gives alice rights on one folder of the shared mailbox.
func (f *sharedMessageFixture) grantFolder(t *testing.T, fid int64, rights uint32) {
	t.Helper()
	st, err := objectstore.Open(f.shared)
	mustNoErr(t, "open", err)
	defer st.Close()
	mustNoErr(t, "grant", st.ModifyPermissions(fid, false, []objectstore.PermissionChange{
		{Op: objectstore.PermAdd, Username: "alice@hermex.test", Rights: rights},
	}))
}

// TestSharedRecallIsAuthoredByTheSharedMailbox proves a message the shared
// mailbox sent is recallable from its Sent Items by a write grantee, because its
// author is the mailbox the request names, not the delegate.
func TestSharedRecallIsAuthoredByTheSharedMailbox(t *testing.T) {
	f := newSharedMessageFixture(t, 0, false)
	f.grantFolder(t, int64(mapi.PrivateFIDSentItems), mapi.RightsEditor)
	st, err := objectstore.Open(f.shared)
	mustNoErr(t, "open", err)
	raw := "From: team@hermex.test\r\nTo: nobody@example.org\r\nSubject: sent\r\nMessage-ID: <r1@hermex.test>\r\n\r\nbody"
	_, err = st.AppendMessage(int64(mapi.PrivateFIDSentItems), []byte(raw), time.Now(), objectstore.FlagSeen)
	st.Close()
	mustNoErr(t, "append", err)
	rec := f.do(http.MethodPost, "/api/v1/mail/recall?id=sent:1&owner=team@hermex.test", "")
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
}

// TestSharedProposalNeedsASendGrant proves a delegate proposes a new meeting time
// for the shared mailbox only under a send-as or send-on-behalf grant, the same
// decision every send path takes.
func TestSharedProposalNeedsASendGrant(t *testing.T) {
	f := newSharedMessageFixture(t, mapi.RightsEditor, false)
	body := `{"id":"inbox:1","start":"2026-09-08T11:00:00Z","end":"2026-09-08T12:00:00Z"}`
	rec := f.do(http.MethodPost, "/api/v1/mail/propose-time?owner=team@hermex.test", body)
	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403; body=%s", rec.Code, rec.Body.String())
	}
}

// fileInvite files in the shared Inbox an invitation bob organizes and returns its
// id.
func (f *sharedMessageFixture) fileInvite(t *testing.T) string {
	t.Helper()
	st, err := objectstore.Open(f.shared)
	mustNoErr(t, "open", err)
	defer st.Close()
	info, err := st.AppendMessage(int64(mapi.PrivateFIDInbox), inviteMail("shared-invite@hermex.test"), time.Now(), 0)
	mustNoErr(t, "file the invite", err)
	return "inbox:" + strconv.FormatUint(uint64(info.UID), 10)
}

// TestSharedRSVPNeedsASendGrant proves a delegate answers an invitation in the
// shared mailbox only under a send-as or send-on-behalf grant, because the answer
// goes to the organizer from that mailbox. It used to go out under the mailbox's
// name for anyone who could change its Inbox. A refused answer records nothing.
func TestSharedRSVPNeedsASendGrant(t *testing.T) {
	f := newSharedMessageFixture(t, mapi.RightsEditor, false)
	id := f.fileInvite(t)
	rec := f.do(http.MethodPost, "/api/v1/mail/rsvp?owner=team@hermex.test", `{"id":"`+id+`","response":"accept"}`)
	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403; body=%s", rec.Code, rec.Body.String())
	}
	wantEq(t, "appointments after a refused answer", calendarCount(t, f.shared), 0)
}

// TestSharedRSVPNamesTheDelegate follows a delegate's answer to the organizer. It is
// the shared mailbox's answer, so it is from that mailbox; under a send-on-behalf
// grant it names the delegate as its Sender and in the attendee's SENT-BY, and under
// a send-as grant it names the mailbox alone.
func TestSharedRSVPNamesTheDelegate(t *testing.T) {
	for _, c := range []struct {
		name     string
		grant    func(*objectstore.Store, []string) error
		sender   string
		attendee string
	}{
		{"on behalf", (*objectstore.Store).SetSendOnBehalf, "Sender: <alice@hermex.test>",
			`ATTENDEE;SENT-BY="mailto:alice@hermex.test";PARTSTAT=ACCEPTED:mailto:team@hermex.test`},
		{"send as", (*objectstore.Store).SetSendAs, "",
			"ATTENDEE;PARTSTAT=ACCEPTED:mailto:team@hermex.test"},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newSharedMessageFixture(t, mapi.RightsEditor, false)
			bob := t.TempDir()
			accounts := directory.StaticAccounts{
				"alice@hermex.test": {Password: "pw", MailboxPath: f.own},
				"team@hermex.test":  {Shared: true, MailboxPath: f.shared},
				"bob@hermex.test":   {Password: "pw", MailboxPath: bob},
			}
			f.srv = NewServer(accounts, accounts, nil, "mail.hermex.test", []byte("shared-message-test-secret"), "", false)
			st, err := objectstore.Open(f.shared)
			mustNoErr(t, "open", err)
			mustNoErr(t, "grant", c.grant(st, []string{"alice@hermex.test"}))
			st.Close()
			id := f.fileInvite(t)

			rec := f.do(http.MethodPost, "/api/v1/mail/rsvp?owner=team@hermex.test", `{"id":"`+id+`","response":"accept"}`)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d; body=%s", rec.Code, rec.Body.String())
			}
			answer := lastOf(t, folderMail(t, bob, int64(mapi.PrivateFIDInbox)), 1)
			head, _, _ := strings.Cut(answer, "\r\n\r\n")
			wantContains(t, "answer header", head, "From: <team@hermex.test>")
			if c.sender == "" {
				if strings.Contains(head, "Sender:") {
					t.Errorf("a send-as answer names a Sender:\n%s", head)
				}
			} else {
				wantContains(t, "answer header", head, c.sender)
			}
			ics := findCalendarPart(mime.ParseStructure([]byte(answer)))
			wantContains(t, "answer calendar", strings.Join(oxcical.ContentLines(ics), "\n"), c.attendee)
		})
	}
}
