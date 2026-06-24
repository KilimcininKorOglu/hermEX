package ews

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"hermex/internal/directory"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// subServer builds an EWS server over a single account and returns it with a
// session for that user and the mailbox path (so a test can seed the store).
func subServer(t *testing.T) (*Server, *session, string) {
	t.Helper()
	path := t.TempDir()
	accs := directory.StaticAccounts{testUser: {Password: testPass, MailboxPath: path}}
	srv := NewServer(accs, accs, "mail.hermex.test")
	// Touch the store so the folder tree exists before the first subscribe.
	if st, err := objectstore.Open(path); err != nil {
		t.Fatal(err)
	} else {
		st.Close()
	}
	return srv, &session{user: testUser, mailbox: path}, path
}

// subscribeInner builds a PullSubscriptionRequest. allFolders selects the whole
// mailbox; otherwise the named distinguished folder is the scope.
func subscribeInner(allFolders bool, distinguished string, eventTypes ...string) string {
	attr, folders := "", ""
	if allFolders {
		attr = ` SubscribeToAllFolders="true"`
	} else if distinguished != "" {
		folders = `<t:FolderIds><t:DistinguishedFolderId Id="` + distinguished + `"/></t:FolderIds>`
	}
	var ets strings.Builder
	for _, e := range eventTypes {
		ets.WriteString(`<t:EventType>`)
		ets.WriteString(e)
		ets.WriteString(`</t:EventType>`)
	}
	return `<Subscribe xmlns="` + nsMessages + `" xmlns:t="` + nsTypes + `">` +
		`<t:PullSubscriptionRequest` + attr + `>` + folders +
		`<t:EventTypes>` + ets.String() + `</t:EventTypes><t:Timeout>30</t:Timeout>` +
		`</t:PullSubscriptionRequest></Subscribe>`
}

// subscribe runs handleSubscribe and returns the issued SubscriptionId.
func subscribe(t *testing.T, srv *Server, sess *session, inner string) string {
	t.Helper()
	rec := httptest.NewRecorder()
	srv.handleSubscribe(rec, []byte(inner), sess)
	body := rec.Body.String()
	const open, closeTag = "<SubscriptionId>", "</SubscriptionId>"
	i, j := strings.Index(body, open), strings.Index(body, closeTag)
	if i < 0 || j < 0 {
		t.Fatalf("Subscribe issued no SubscriptionId: %s", body)
	}
	return body[i+len(open) : j]
}

// getEvents runs handleGetEvents for a subscription id and returns the response.
func getEvents(t *testing.T, srv *Server, sess *session, id string) string {
	t.Helper()
	rec := httptest.NewRecorder()
	srv.handleGetEvents(rec, []byte(`<GetEvents xmlns="`+nsMessages+`"><SubscriptionId>`+id+`</SubscriptionId></GetEvents>`), sess)
	return rec.Body.String()
}

// seedInbox appends a message to the Inbox and returns its id and IMAP uid.
func seedInbox(t *testing.T, path, subject string) (int64, uint32) {
	t.Helper()
	st, err := objectstore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	raw := []byte("From: a@x.test\r\nTo: b@x.test\r\nSubject: " + subject +
		"\r\nDate: Wed, 15 Nov 2023 10:13:20 +0000\r\n\r\nbody\r\n")
	info, err := st.AppendMessage(mapi.PrivateFIDInbox, raw, time.Unix(1700000000, 0), 0)
	if err != nil {
		t.Fatal(err)
	}
	return info.ID, info.UID
}

// TestSubscribeBaselineNoFlood confirms a message that already exists when the
// subscription is created is NOT reported as a create on the first poll — the
// baseline-at-registration invariant.
func TestSubscribeBaselineNoFlood(t *testing.T) {
	srv, sess, path := subServer(t)
	seedInbox(t, path, "pre-existing")
	id := subscribe(t, srv, sess, subscribeInner(true, "", "CreatedEvent"))

	out := getEvents(t, srv, sess, id)
	if !strings.Contains(out, "StatusEvent") {
		t.Errorf("first poll must be just a StatusEvent (no flood of pre-existing items): %s", out)
	}
	if strings.Contains(out, "CreatedEvent") {
		t.Errorf("pre-existing message reported as a create: %s", out)
	}
}

// TestGetEventsCreated confirms a message delivered after the subscription is
// reported as a CreatedEvent carrying an ItemId.
func TestGetEventsCreated(t *testing.T) {
	srv, sess, path := subServer(t)
	id := subscribe(t, srv, sess, subscribeInner(true, "", "CreatedEvent"))
	seedInbox(t, path, "new mail")

	out := getEvents(t, srv, sess, id)
	if !strings.Contains(out, "CreatedEvent") || !strings.Contains(out, "<ItemId Id=") {
		t.Errorf("a newly delivered message must yield a CreatedEvent with an ItemId: %s", out)
	}
}

// TestSubscribeScopeIsolation confirms an explicit single-folder subscription
// reports changes only in that folder: a message delivered to a sibling folder
// is invisible, while one in the subscribed folder fires. A whole-store
// subscription cannot catch a scoping bug because it watches everything.
func TestSubscribeScopeIsolation(t *testing.T) {
	srv, sess, path := subServer(t)
	id := subscribe(t, srv, sess, subscribeInner(false, "inbox", "CreatedEvent"))

	// A message outside the subscribed folder must not fire.
	st, err := objectstore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	raw := []byte("From: a@x.test\r\nTo: b@x.test\r\nSubject: elsewhere\r\n" +
		"Date: Wed, 15 Nov 2023 10:13:20 +0000\r\n\r\nbody\r\n")
	if _, err := st.AppendMessage(mapi.PrivateFIDSentItems, raw, time.Unix(1700000000, 0), 0); err != nil {
		st.Close()
		t.Fatal(err)
	}
	st.Close()
	if out := getEvents(t, srv, sess, id); strings.Contains(out, "CreatedEvent") {
		t.Errorf("a create outside the subscribed folder must not fire: %s", out)
	}

	// A message in the subscribed folder fires.
	seedInbox(t, path, "in scope")
	if out := getEvents(t, srv, sess, id); !strings.Contains(out, "CreatedEvent") {
		t.Errorf("a create in the subscribed folder must fire: %s", out)
	}
}

// TestGetEventsModified confirms a read-state flip (the change-number signal the
// poll folds in) is reported as a ModifiedEvent.
func TestGetEventsModified(t *testing.T) {
	srv, sess, path := subServer(t)
	mid, _ := seedInbox(t, path, "to modify")
	id := subscribe(t, srv, sess, subscribeInner(true, "", "ModifiedEvent"))

	st, err := objectstore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetMessageReadState(mid, true); err != nil {
		t.Fatal(err)
	}
	st.Close()

	out := getEvents(t, srv, sess, id)
	if !strings.Contains(out, "ModifiedEvent") {
		t.Errorf("a read-state change must yield a ModifiedEvent: %s", out)
	}
}

// TestGetEventsDeleted confirms a deleted message is reported as a DeletedEvent.
func TestGetEventsDeleted(t *testing.T) {
	srv, sess, path := subServer(t)
	_, uid := seedInbox(t, path, "to delete")
	id := subscribe(t, srv, sess, subscribeInner(true, "", "DeletedEvent"))

	st, err := objectstore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteMessage(int64(mapi.PrivateFIDInbox), uid); err != nil {
		t.Fatal(err)
	}
	st.Close()

	out := getEvents(t, srv, sess, id)
	if !strings.Contains(out, "DeletedEvent") {
		t.Errorf("a deleted message must yield a DeletedEvent: %s", out)
	}
}

// TestGetEventsStatusOnEmpty confirms a poll with no changes returns a single
// StatusEvent (the heartbeat), not an empty notification.
func TestGetEventsStatusOnEmpty(t *testing.T) {
	srv, sess, _ := subServer(t)
	id := subscribe(t, srv, sess, subscribeInner(true, "", "CreatedEvent"))
	out := getEvents(t, srv, sess, id)
	if !strings.Contains(out, "StatusEvent") {
		t.Errorf("an idle poll must carry a StatusEvent: %s", out)
	}
}

// TestEventTypeMaskGating confirms a subscription to CreatedEvent only does not
// report a modify — the event-type mask gates production.
func TestEventTypeMaskGating(t *testing.T) {
	srv, sess, path := subServer(t)
	mid, _ := seedInbox(t, path, "modify me")
	id := subscribe(t, srv, sess, subscribeInner(true, "", "CreatedEvent"))

	st, err := objectstore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetMessageReadState(mid, true); err != nil {
		t.Fatal(err)
	}
	st.Close()

	out := getEvents(t, srv, sess, id)
	if strings.Contains(out, "ModifiedEvent") {
		t.Errorf("a Created-only subscription must not report a modify: %s", out)
	}
}

// TestUnsubscribe confirms an Unsubscribe drops the subscription (a later poll is
// invalid) and that unsubscribing an unknown id reports ErrorSubscriptionNotFound.
func TestUnsubscribe(t *testing.T) {
	srv, sess, _ := subServer(t)
	id := subscribe(t, srv, sess, subscribeInner(true, "", "CreatedEvent"))

	rec := httptest.NewRecorder()
	srv.handleUnsubscribe(rec, []byte(`<Unsubscribe xmlns="`+nsMessages+`"><SubscriptionId>`+id+`</SubscriptionId></Unsubscribe>`), sess)
	if !strings.Contains(rec.Body.String(), `ResponseClass="Success"`) {
		t.Errorf("Unsubscribe of a live subscription must succeed: %s", rec.Body.String())
	}

	if out := getEvents(t, srv, sess, id); !strings.Contains(out, "ErrorInvalidSubscription") {
		t.Errorf("a poll after Unsubscribe must be ErrorInvalidSubscription: %s", out)
	}

	rec2 := httptest.NewRecorder()
	srv.handleUnsubscribe(rec2, []byte(`<Unsubscribe xmlns="`+nsMessages+`"><SubscriptionId>Zm9vYmFyMDA=</SubscriptionId></Unsubscribe>`), sess)
	if !strings.Contains(rec2.Body.String(), "ErrorSubscriptionNotFound") {
		t.Errorf("Unsubscribe of an unknown id must be ErrorSubscriptionNotFound: %s", rec2.Body.String())
	}
}

// TestGetEventsCrossUserDenied confirms a poll by a user other than the
// subscription owner is ErrorAccessDenied.
func TestGetEventsCrossUserDenied(t *testing.T) {
	srv, sess, _ := subServer(t)
	id := subscribe(t, srv, sess, subscribeInner(true, "", "CreatedEvent"))

	other := &session{user: "mallory@hermex.test", mailbox: t.TempDir()}
	if out := getEvents(t, srv, other, id); !strings.Contains(out, "ErrorAccessDenied") {
		t.Errorf("a cross-user poll must be ErrorAccessDenied: %s", out)
	}
}

// TestGetEventsExpired confirms a subscription past its timeout is evicted and
// reported as invalid.
func TestGetEventsExpired(t *testing.T) {
	srv, sess, _ := subServer(t)
	id := subscribe(t, srv, sess, subscribeInner(true, "", "CreatedEvent"))

	// Force expiry by backdating the creation past the timeout.
	srv.subMu.Lock()
	srv.subs[id].created = time.Now().Add(-2 * time.Hour)
	srv.subMu.Unlock()

	if out := getEvents(t, srv, sess, id); !strings.Contains(out, "ErrorInvalidSubscription") {
		t.Errorf("an expired subscription must be ErrorInvalidSubscription: %s", out)
	}
	srv.subMu.Lock()
	_, present := srv.subs[id]
	srv.subMu.Unlock()
	if present {
		t.Error("an expired subscription must be evicted")
	}
}
