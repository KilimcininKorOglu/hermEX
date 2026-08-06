package mapihttp

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"hermex/internal/directory"
)

// testStore builds an EMSMDB session store over a throwaway mailbox.
func testStore(t *testing.T) (*sessionStore, directory.StaticAccounts) {
	t.Helper()
	accs := directory.StaticAccounts{testUser: {Password: testPass, MailboxPath: t.TempDir()}}
	return newSessionStore(), accs
}

// TestSweepReclaimsIdleSession proves a session whose client vanished without
// Disconnect is reclaimed once it passes the idle limit, and that the client's
// cookies stop working afterwards. Without this the store keeps the session, and
// with it an open mailbox handle, for the life of the process.
func TestSweepReclaimsIdleSession(t *testing.T) {
	store, accs := testStore(t)
	sid, seq := store.create(testUser, accs[testUser].MailboxPath, accs, nil)

	if n := store.sweep(time.Now(), sessionTTL); n != 0 {
		t.Fatalf("swept %d fresh session(s), want 0", n)
	}

	if n := store.sweep(time.Now().Add(sessionTTL+time.Minute), sessionTTL); n != 1 {
		t.Fatalf("swept %d idle session(s), want 1", n)
	}
	if _, _, code := store.execute(sid, seq, testUser); code != rcInvalidCtxCookie {
		t.Errorf("execute after reclaim = %d, want rcInvalidCtxCookie (%d)", code, rcInvalidCtxCookie)
	}
	if store.lookup(sid) != nil {
		t.Error("lookup still resolves a reclaimed session")
	}
}

// TestReclaimedSessionIsRejectedEndToEnd proves the reclamation reaches the wire:
// a client that connected and then went quiet finds its cookies refused with the
// invalid-context-cookie code, the same answer it gets after a Disconnect, so it
// reconnects instead of hanging on a session the server no longer has.
func TestReclaimedSessionIsRejectedEndToEnd(t *testing.T) {
	accs := directory.StaticAccounts{testUser: {Password: testPass, MailboxPath: t.TempDir()}}
	srv := NewServer(accs, accs, "mail.hermex.test", nil)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	resp := mapiPost(t, ts, "/mapi/emsmdb", "Connect", connectBody(), nil)
	resp.Body.Close()
	sid, seq := cookieByName(resp, "sid"), cookieByName(resp, "sequence")
	if sid == "" || seq == "" {
		t.Fatalf("Connect returned no session cookies (sid=%q seq=%q)", sid, seq)
	}

	if n := srv.sessions.sweep(time.Now().Add(sessionTTL+time.Minute), sessionTTL); n != 1 {
		t.Fatalf("swept %d session(s), want the idle one", n)
	}

	resp = mapiPost(t, ts, "/mapi/emsmdb", "Execute", executeBody(), func(r *http.Request) {
		r.AddCookie(&http.Cookie{Name: "sid", Value: sid})
		r.AddCookie(&http.Cookie{Name: "sequence", Value: seq})
	})
	resp.Body.Close()
	if got, want := resp.Header.Get("X-ResponseCode"), strconv.Itoa(rcInvalidCtxCookie); got != want {
		t.Errorf("X-ResponseCode after reclaim = %q, want %q (invalid context cookie)", got, want)
	}
}

// TestExecuteKeepsASessionAlive proves a client that keeps working is never
// reclaimed: each Execute stamps the session, so the idle clock restarts.
func TestExecuteKeepsASessionAlive(t *testing.T) {
	store, accs := testStore(t)
	sid, seq := store.create(testUser, accs[testUser].MailboxPath, accs, nil)

	// Well past the limit measured from Connect, but the client just executed.
	seq, _, code := store.execute(sid, seq, testUser)
	if code != rcSuccess {
		t.Fatalf("execute = %d, want success", code)
	}
	if n := store.sweep(time.Now(), sessionTTL); n != 0 {
		t.Fatalf("swept %d active session(s), want 0", n)
	}
	if _, _, code := store.execute(sid, seq, testUser); code != rcSuccess {
		t.Errorf("second execute = %d, want success", code)
	}
}

// TestLookupKeepsAParkedWaitAlive proves the notification long-poll stamps the
// session when it parks. A wait holds for most of a minute, and it must not look
// idle while it runs.
func TestLookupKeepsAParkedWaitAlive(t *testing.T) {
	store, accs := testStore(t)
	sid, _ := store.create(testUser, accs[testUser].MailboxPath, accs, nil)

	if store.lookup(sid) == nil {
		t.Fatal("lookup did not resolve a live session")
	}
	if n := store.sweep(time.Now(), sessionTTL); n != 0 {
		t.Errorf("swept %d session(s) that just parked a wait, want 0", n)
	}
}

// TestNspiSweepReclaimsIdleBinding proves the address-book store reclaims a
// binding whose client never sent Unbind, and leaves a live one alone.
func TestNspiSweepReclaimsIdleBinding(t *testing.T) {
	store := newNspiSessionStore()
	sid, seq := store.bind(testUser)

	if n := store.sweep(time.Now(), sessionTTL); n != 0 {
		t.Fatalf("swept %d fresh binding(s), want 0", n)
	}
	if n := store.sweep(time.Now().Add(sessionTTL+time.Minute), sessionTTL); n != 1 {
		t.Fatalf("swept %d idle binding(s), want 1", n)
	}
	if _, code := store.validate(sid, seq, testUser); code != rcInvalidCtxCookie {
		t.Errorf("validate after reclaim = %d, want rcInvalidCtxCookie (%d)", code, rcInvalidCtxCookie)
	}
}

// TestNspiValidateKeepsABindingAlive proves a working address-book client is not
// reclaimed underneath itself.
func TestNspiValidateKeepsABindingAlive(t *testing.T) {
	store := newNspiSessionStore()
	sid, seq := store.bind(testUser)

	seq, code := store.validate(sid, seq, testUser)
	if code != rcSuccess {
		t.Fatalf("validate = %d, want success", code)
	}
	if n := store.sweep(time.Now(), sessionTTL); n != 0 {
		t.Fatalf("swept %d active binding(s), want 0", n)
	}
	if _, code := store.validate(sid, seq, testUser); code != rcSuccess {
		t.Errorf("second validate = %d, want success", code)
	}
}
