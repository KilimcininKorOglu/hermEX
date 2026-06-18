package mapihttp

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"hermex/internal/directory"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
	"hermex/internal/rop"
)

// notifyTestServer builds a MAPI/HTTP server over a single account at a known
// mailbox path (so a test can deliver into the shared store directly) with a short
// notification hold so the timeout path is fast. It returns both the HTTP test
// server and the underlying *Server, so a test can reach the live session.
func notifyTestServer(t *testing.T, mailbox string) (*httptest.Server, *Server) {
	t.Helper()
	accs := directory.StaticAccounts{testUser: {Password: testPass, MailboxPath: mailbox}}
	srv := NewServer(accs, accs, "mail.hermex.test")
	srv.notifyWait = 200 * time.Millisecond
	srv.notifyCadence = 10 * time.Millisecond
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts, srv
}

// notifyConnect runs Connect and returns the sid cookie.
func notifyConnect(t *testing.T, ts *httptest.Server) string {
	t.Helper()
	conn := mapiPost(t, ts, "/mapi/emsmdb", "Connect", connectBody(), nil)
	conn.Body.Close()
	sid := cookieByName(conn, "sid")
	if sid == "" {
		t.Fatal("Connect did not set a sid cookie")
	}
	return sid
}

// notificationWaitFlags posts a NotificationWait and returns its EventPending flags,
// asserting the response carries the full 4-uint32 body (Status, Error, EventPending,
// AuxiliaryBufferSize) — the trailing AuxiliaryBufferSize is what the old stub
// omitted.
func notificationWaitFlags(t *testing.T, ts *httptest.Server, sid string) uint32 {
	t.Helper()
	resp := mapiPost(t, ts, "/mapi/emsmdb", "NotificationWait", nil, func(r *http.Request) {
		r.AddCookie(&http.Cookie{Name: "sid", Value: sid})
	})
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	_, payload, found := bytes.Cut(body, []byte("\r\n\r\n"))
	if !found {
		t.Fatalf("NotificationWait response missing meta preamble: %q", body)
	}
	if len(payload) != 16 {
		t.Fatalf("NotificationWait body = %d bytes, want 16 (Status|Error|EventPending|AuxBufSize): % x", len(payload), payload)
	}
	if status := binary.LittleEndian.Uint32(payload[0:]); status != rcSuccess {
		t.Errorf("StatusCode = %d, want 0", status)
	}
	return binary.LittleEndian.Uint32(payload[8:])
}

// subscribeInbox sets up a folder-scoped created subscription on a live session by
// driving its ROP layer directly (Logon to open the store, then RegisterNotification
// on the Inbox), mirroring what an Outlook client does over two Execute batches.
func subscribeInbox(t *testing.T, sess *rop.Session) {
	t.Helper()
	logon := []byte{0xFE, 0x00, 0x00, 0x01} // RopLogon, LogonId, InputHandleIndex, LogonFlags=Private
	logon = binary.LittleEndian.AppendUint32(logon, 0)
	logon = binary.LittleEndian.AppendUint32(logon, 0)
	logon = binary.LittleEndian.AppendUint16(logon, 0)
	_, h := sess.Dispatch(logon, []uint32{0xFFFFFFFF})
	logonH := h[0]
	if logonH == 0xFFFFFFFF {
		t.Fatal("logon handle not set")
	}

	// RopRegisterNotification: OutputHandleIndex=1, NotificationTypes=ObjectCreated,
	// Reserved=0, WantWholeStore=0, then the Inbox FolderId and MessageId=0.
	reg := []byte{0x29, 0x00, 0x00, 0x01, 0x04, 0x00, 0x00}
	reg = binary.LittleEndian.AppendUint64(reg, uint64(mapi.MakeEIDEx(1, mapi.PrivateFIDInbox)))
	reg = binary.LittleEndian.AppendUint64(reg, 0)
	sess.Dispatch(reg, []uint32{logonH, 0xFFFFFFFF})
}

// TestNotificationWaitTimeout: a quiet mailbox holds the wait for its interval and
// returns EventPending=0.
func TestNotificationWaitTimeout(t *testing.T) {
	ts, _ := notifyTestServer(t, t.TempDir())
	sid := notifyConnect(t, ts)
	if flags := notificationWaitFlags(t, ts, sid); flags != 0 {
		t.Errorf("EventPending flags = %d, want 0 (no subscription, no change)", flags)
	}
}

// TestNotificationWaitPending: once a subscribed folder gains a message, the wait
// returns EventPending=1 — the wake signal that tells the client to Execute and drain
// the RopNotify.
func TestNotificationWaitPending(t *testing.T) {
	mailbox := t.TempDir()
	ts, srv := notifyTestServer(t, mailbox)
	sid := notifyConnect(t, ts)

	// Subscribe on the live session that the NotificationWait will poll.
	sc := srv.sessions.lookup(sid)
	if sc == nil {
		t.Fatal("session not found after Connect")
	}
	subscribeInbox(t, sc.ropSess)

	// Deliver a message through a separate store handle (the cross-process MTA).
	st, err := objectstore.Open(mailbox)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	_, err = st.AppendMessage(int64(mapi.PrivateFIDInbox), []byte("From: a@test\r\n\r\nhi\r\n"), time.Unix(1700000000, 0), 0)
	st.Close()
	if err != nil {
		t.Fatalf("append: %v", err)
	}

	if flags := notificationWaitFlags(t, ts, sid); flags != flagNotificationPending {
		t.Errorf("EventPending flags = %d, want %d (a created message is pending)", flags, flagNotificationPending)
	}
}

// TestNotificationWaitWakesMidWait proves the long-poll's reason for being: a wait
// that begins on a quiet (but subscribed) mailbox is woken by a delivery that lands
// DURING the wait, not only by one that preceded it. The wait must return
// EventPending=1; a timeout would return 0, so the flag alone proves the mid-wait
// poll detected the delivery rather than the wait expiring. The delivery comes from a
// separate store handle, as a separate daemon's MTA would.
func TestNotificationWaitWakesMidWait(t *testing.T) {
	mailbox := t.TempDir()
	ts, srv := notifyTestServer(t, mailbox)
	sid := notifyConnect(t, ts)
	sc := srv.sessions.lookup(sid)
	if sc == nil {
		t.Fatal("session not found after Connect")
	}
	subscribeInbox(t, sc.ropSess)

	flags := make(chan uint32, 1)
	go func() { flags <- notificationWaitFlags(t, ts, sid) }()

	// Deliver after the wait has begun polling a quiet mailbox, so the wake can only
	// come from a mid-wait poll detecting this delivery.
	time.Sleep(40 * time.Millisecond)
	st, err := objectstore.Open(mailbox)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	_, err = st.AppendMessage(int64(mapi.PrivateFIDInbox), []byte("From: a@test\r\n\r\nhi\r\n"), time.Unix(1700000000, 0), 0)
	st.Close()
	if err != nil {
		t.Fatalf("append: %v", err)
	}

	select {
	case got := <-flags:
		if got != flagNotificationPending {
			t.Errorf("mid-wait delivery: EventPending = %d, want %d (the long-poll did not wake on the delivery)", got, flagNotificationPending)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("NotificationWait never returned")
	}
}

// TestWaitForNotificationContextCancel proves the loop bails as soon as the client
// drops the connection, rather than holding the full interval.
func TestWaitForNotificationContextCancel(t *testing.T) {
	accs := directory.StaticAccounts{testUser: {Password: testPass, MailboxPath: t.TempDir()}}
	srv := NewServer(accs, accs, "mail.hermex.test")
	srv.notifyWait = 30 * time.Second // long, so only ctx cancellation can end the wait quickly
	srv.notifyCadence = 10 * time.Millisecond

	sess := rop.NewSession(t.TempDir(), nil, "")
	defer sess.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	done := make(chan bool, 1)
	go func() { done <- srv.waitForNotification(ctx, sess) }()
	select {
	case pending := <-done:
		if pending {
			t.Error("waitForNotification = true on a cancelled context, want false")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("waitForNotification did not return on context cancel (ignored r.Context().Done())")
	}
}
