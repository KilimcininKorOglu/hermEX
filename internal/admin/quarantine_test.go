package admin

import (
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"hermex/internal/directory"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// TestQuarantineListReleaseDelete proves the admin quarantine view lists a user's
// Junk messages by metadata, that release actually moves a message to the inbox
// (not merely drops it from Junk), and that delete removes a message from both
// folders. The "present in the inbox after release" check is the one that
// distinguishes a real move from a silent drop.
func TestQuarantineListReleaseDelete(t *testing.T) {
	tmp := t.TempDir()
	mbox := filepath.Join(tmp, "alice")

	st, err := objectstore.Open(mbox)
	mustNoErr(t, err, "open mailbox")
	when := time.Now()
	_, err = st.AppendMessage(int64(mapi.PrivateFIDJunk), []byte("Subject: spam one\r\n\r\nbuy now"), when, 0)
	mustNoErr(t, err, "append the first junk message")
	_, err = st.AppendMessage(int64(mapi.PrivateFIDJunk), []byte("Subject: spam two\r\n\r\ndiscount"), when, 0)
	mustNoErr(t, err, "append the second junk message")
	junk, err := st.ListMessages(int64(mapi.PrivateFIDJunk))
	mustNoErr(t, err, "list the Junk folder")
	st.Close()
	if len(junk) != 2 {
		t.Fatalf("seeded Junk has %d messages, want 2", len(junk))
	}
	releaseUID, deleteUID := junk[0].UID, junk[1].UID

	d := &fakeDir{
		authOK:     true,
		uid:        7,
		roles:      []directory.AdminRole{{Role: directory.AdminSystem}},
		userDetail: directory.UserDetail{Maildir: mbox},
	}
	ts := adminServer(t, d)
	session, csrf := loginCookies(t, ts)

	// List shows both Junk messages by subject (metadata only).
	page := wantBody(t, authedGET(t, ts, "/admin/ui/users/alice@test/quarantine", session),
		http.StatusOK, "quarantine list")
	wantContains(t, page, "spam one", "the list carries the first seeded subject")
	wantContains(t, page, "spam two", "the list carries the second seeded subject")

	// Release the first message → it leaves Junk and lands in the inbox.
	rel := htmxPOST(t, ts, "/admin/ui/users/alice@test/quarantine/release", session, csrf,
		url.Values{"uid": {strconv.FormatUint(uint64(releaseUID), 10)}})
	rel.Body.Close()

	junkAfter, inboxAfter := junkAndInbox(t, mbox)
	wantEq(t, len(junkAfter), 1, "Junk messages after the release")
	wantEq(t, len(inboxAfter), 1, "inbox messages after the release")
	wantEq(t, inboxAfter[0].Subject, "spam one", "the released message's subject in the inbox")

	// Delete the remaining message → gone from Junk, inbox untouched.
	del := htmxPOST(t, ts, "/admin/ui/users/alice@test/quarantine/delete", session, csrf,
		url.Values{"uid": {strconv.FormatUint(uint64(deleteUID), 10)}})
	del.Body.Close()

	junkFinal, inboxFinal := junkAndInbox(t, mbox)
	wantEq(t, len(junkFinal), 0, "Junk messages after the delete")
	wantEq(t, len(inboxFinal), 1, "inbox messages after the delete (it must not be touched)")
}

// TestQuarantineActionsRefuseAnUnauthorizedCaller sends release and delete as a
// domain admin and as a system admin without the CSRF token. The quarantine routes
// act on any user's mailbox by address, so each must answer 403 and leave Junk as it
// was.
func TestQuarantineActionsRefuseAnUnauthorizedCaller(t *testing.T) {
	mbox := filepath.Join(t.TempDir(), "alice")
	st, err := objectstore.Open(mbox)
	mustNoErr(t, err, "open mailbox")
	_, err = st.AppendMessage(int64(mapi.PrivateFIDJunk), []byte("Subject: held\r\n\r\nx"), time.Now(), 0)
	mustNoErr(t, err, "append the junk message")
	junk, err := st.ListMessages(int64(mapi.PrivateFIDJunk))
	mustNoErr(t, err, "list the Junk folder")
	st.Close()
	uid := url.Values{"uid": {strconv.FormatUint(uint64(junk[0].UID), 10)}}

	domainAdmin := &fakeDir{authOK: true, uid: 7, userDetail: directory.UserDetail{Maildir: mbox},
		roles: []directory.AdminRole{{Role: directory.AdminDomain, ScopeID: 1}}}
	ts := adminServer(t, domainAdmin)
	session, csrf := loginCookies(t, ts)
	for _, action := range []string{"release", "delete"} {
		wantStatus(t, htmxPOST(t, ts, "/admin/ui/users/alice@test/quarantine/"+action, session, csrf, uid),
			http.StatusForbidden, "domain admin "+action)
	}

	system := &fakeDir{authOK: true, uid: 7, userDetail: directory.UserDetail{Maildir: mbox},
		roles: []directory.AdminRole{{Role: directory.AdminSystem}}}
	ts = adminServer(t, system)
	session, csrf = loginCookies(t, ts)
	for _, action := range []string{"release", "delete"} {
		// A cross-site request carries the victim's cookies but cannot read the CSRF
		// cookie, so its header is missing or wrong.
		for _, header := range []string{"", "forged"} {
			req := newRequest(t, "POST", ts.URL+"/admin/ui/users/alice@test/quarantine/"+action,
				strings.NewReader(uid.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			if header != "" {
				req.Header.Set(csrfHeader, header)
			}
			req.AddCookie(&http.Cookie{Name: sessionCookie, Value: session})
			req.AddCookie(&http.Cookie{Name: csrfCookie, Value: csrf})
			resp, err := http.DefaultClient.Do(req)
			mustNoErr(t, err, "send the forged request")
			wantStatus(t, resp, http.StatusForbidden, "CSRF header "+strconv.Quote(header)+" on "+action)
		}
	}

	junkAfter, inboxAfter := junkAndInbox(t, mbox)
	wantEq(t, len(junkAfter), 1, "Junk messages after the refused actions")
	wantEq(t, len(inboxAfter), 0, "inbox messages after the refused actions")
}

// junkAndInbox reopens a mailbox and lists what its Junk and inbox hold.
func junkAndInbox(t *testing.T, mbox string) (junk, inbox []objectstore.MessageInfo) {
	t.Helper()
	st, err := objectstore.Open(mbox)
	mustNoErr(t, err, "reopen mailbox")
	defer st.Close()
	junk, err = st.ListMessages(int64(mapi.PrivateFIDJunk))
	mustNoErr(t, err, "list the Junk folder")
	inbox, err = st.ListMessages(int64(mapi.PrivateFIDInbox))
	mustNoErr(t, err, "list the Inbox")
	return junk, inbox
}
