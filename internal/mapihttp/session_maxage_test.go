package mapihttp

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"hermex/internal/directory"
)

// backdateSession moves a session's Connect time into the past, standing in for
// a client that has held the same session for days.
func backdateSession(t *testing.T, store *sessionStore, sid string, age time.Duration) {
	t.Helper()
	store.mu.Lock()
	defer store.mu.Unlock()
	c, ok := store.m[sid]
	if !ok {
		t.Fatal("no such session")
	}
	c.created = c.created.Add(-age)
}

// TestBusySessionIsRefusedAtItsAbsoluteLifetime proves activity no longer buys a
// session unlimited life. Idle reclamation never reaches a client that keeps
// polling, so a single Outlook left running pinned its ROP handle table and open
// mailbox store for as long as the process lived, and the table only ever grew.
func TestBusySessionIsRefusedAtItsAbsoluteLifetime(t *testing.T) {
	store, accs := testStore(t)
	sid, seq := store.create(testUser, accs[testUser].MailboxPath, accs, nil, nil)
	backdateSession(t, store, sid, sessionMaxAge+time.Minute)

	// The client is not idle: it executed a moment ago and executes again now.
	if _, _, code := store.execute(sid, seq, testUser); code != rcInvalidCtxCookie {
		t.Errorf("execute at the lifetime cap = %d, want rcInvalidCtxCookie (%d)", code, rcInvalidCtxCookie)
	}
	if store.lookup(sid, testUser) != nil {
		t.Error("the notification long-poll still resolves a session past its lifetime")
	}
}

// TestBusySessionIsSweptAtItsAbsoluteLifetime proves the memory is actually
// returned, not merely refused: refusing the cookies while keeping the ROP table
// and the open store would leave the leak the cap exists to bound.
func TestBusySessionIsSweptAtItsAbsoluteLifetime(t *testing.T) {
	store, accs := testStore(t)
	sid, _ := store.create(testUser, accs[testUser].MailboxPath, accs, nil, nil)
	backdateSession(t, store, sid, sessionMaxAge+time.Minute)

	// now is the session's lastSeen, so the idle rule alone would keep it.
	if n := store.sweep(time.Now(), sessionTTL); n != 1 {
		t.Fatalf("swept %d session(s) past the lifetime cap, want 1", n)
	}
	store.mu.Lock()
	_, still := store.m[sid]
	store.mu.Unlock()
	if still {
		t.Error("the session is still in the store, so its handle table was not released")
	}
}

// TestSessionBelowTheCapIsUnaffected proves the cap does not disturb ordinary
// use: a session younger than the limit keeps working, so a client is not made
// to re-Connect on every request.
func TestSessionBelowTheCapIsUnaffected(t *testing.T) {
	store, accs := testStore(t)
	sid, seq := store.create(testUser, accs[testUser].MailboxPath, accs, nil, nil)
	backdateSession(t, store, sid, sessionMaxAge-time.Hour)

	if _, _, code := store.execute(sid, seq, testUser); code != rcSuccess {
		t.Errorf("execute below the cap = %d, want success", code)
	}
}

// TestZeroMaxAgeDisablesTheCap pins the documented meaning of a zero lifetime,
// so a store built without one keeps every session until it goes idle.
func TestZeroMaxAgeDisablesTheCap(t *testing.T) {
	accs := directory.StaticAccounts{testUser: {Password: testPass, MailboxPath: t.TempDir()}}
	store := newSessionStore(0)
	sid, seq := store.create(testUser, accs[testUser].MailboxPath, accs, nil, nil)
	backdateSession(t, store, sid, 10*365*24*time.Hour)

	if _, _, code := store.execute(sid, seq, testUser); code != rcSuccess {
		t.Errorf("execute with no cap = %d, want success", code)
	}
}

// TestNotificationWaitRefusesAnotherUsersSession proves the long-poll checks who
// is asking. It runs outside the Execute sequence and resolved a session by sid
// alone, so any authenticated account holding another user's sid could park a
// wait on that mailbox and be told whenever it changed.
func TestNotificationWaitRefusesAnotherUsersSession(t *testing.T) {
	const otherUser = "mallory@hermex.test"
	accs := directory.StaticAccounts{
		testUser:  {Password: testPass, MailboxPath: t.TempDir()},
		otherUser: {Password: testPass, MailboxPath: t.TempDir()},
	}
	srv := NewServer(accs, accs, "mail.hermex.test", nil)
	// The owner's wait below has nothing to report and would otherwise hold for
	// the full production window; only the accept/refuse decision is under test.
	srv.notifyWait = 200 * time.Millisecond
	srv.notifyCadence = 10 * time.Millisecond
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	resp := mapiPost(t, ts, "/mapi/emsmdb", "Connect", connectBody(), nil)
	resp.Body.Close()
	sid := cookieByName(resp, "sid")
	if sid == "" {
		t.Fatal("Connect returned no sid")
	}

	resp = mapiPost(t, ts, "/mapi/emsmdb", "NotificationWait", nil, func(r *http.Request) {
		r.SetBasicAuth(otherUser, testPass) // authenticated, but not the session's owner
		r.AddCookie(&http.Cookie{Name: "sid", Value: sid})
	})
	resp.Body.Close()
	if got, want := resp.Header.Get("X-ResponseCode"), strconv.Itoa(rcInvalidCtxCookie); got != want {
		t.Errorf("X-ResponseCode = %q, want %q; another account parked a wait on this mailbox", got, want)
	}

	// The owner is unaffected.
	resp = mapiPost(t, ts, "/mapi/emsmdb", "NotificationWait", nil, func(r *http.Request) {
		r.AddCookie(&http.Cookie{Name: "sid", Value: sid})
	})
	resp.Body.Close()
	if got, want := resp.Header.Get("X-ResponseCode"), strconv.Itoa(rcSuccess); got != want {
		t.Errorf("the owner's wait = %q, want %q", got, want)
	}
}
