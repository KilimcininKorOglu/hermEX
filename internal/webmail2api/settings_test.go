package webmail2api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"hermex/internal/directory"
	"hermex/internal/objectstore"
)

// TestSettingsConcurrentPutsDoNotLoseUpdates proves concurrent PUTs to different
// settings keys never drop one another. Each PUT is a read-modify-write of one
// shared JSON blob; without serialization two racing writers read the same starting
// blob and the last write overwrites the other's key. All keys must survive.
func TestSettingsConcurrentPutsDoNotLoseUpdates(t *testing.T) {
	dir := t.TempDir()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	mustClose(t, st)

	secret := []byte("settings-race-secret")
	srv := NewServer(directory.StaticAccounts{}, directory.StaticAccounts{}, nil, "mail.hermex.test", secret, "", false)
	token := mustMintToken(t, secret, sessionClaims{Email: "alice@hermex.test", Mailbox: dir, Exp: time.Now().Add(time.Hour).Unix()})

	const n = 40
	var wg sync.WaitGroup
	start := make(chan struct{})
	codes := make([]int, n)
	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			key := fmt.Sprintf("key%d", i)
			req := httptest.NewRequest(http.MethodPut, "/api/v1/settings/x", nil)
			req.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
			rec := httptest.NewRecorder()
			<-start // release all goroutines together to widen the race window
			srv.withSettings(rec, req, func(_ *objectstore.Store, m map[string]json.RawMessage) (any, bool) {
				m[key] = json.RawMessage(`"v"`)
				return map[string]bool{"ok": true}, true
			})
			codes[i] = rec.Code
		}(i)
	}
	close(start)
	wg.Wait()
	for i, c := range codes {
		if c != http.StatusOK {
			t.Errorf("writer %d answered %d", i, c)
		}
	}

	st2, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer mustClose(t, st2)
	got, ok := sharedSettings(st2)
	if !ok {
		t.Fatal("read the shared settings")
	}
	missing := 0
	for i := range n {
		if _, ok := got[fmt.Sprintf("key%d", i)]; !ok {
			missing++
		}
	}
	if missing != 0 {
		t.Errorf("%d of %d concurrent settings keys were lost to a lost-update race", missing, n)
	}
}

// TestARefusedSettingsWriteSavesNothing: a writer that refuses (returns false)
// after changing the map leaves the stored blob as it was, and a request with
// no session never reaches the writer.
func TestARefusedSettingsWriteSavesNothing(t *testing.T) {
	dir := freshMailbox(t)
	st := openMailbox(t, dir)
	mustNoErr(t, "seed", st.SetWebmailSettings(`{"theme":"dark"}`))
	secret := []byte("settings-refuse-secret")
	srv := NewServer(directory.StaticAccounts{}, directory.StaticAccounts{}, nil, "mail.hermex.test", secret, "", false)
	token := mustMintToken(t, secret, sessionClaims{Email: "alice@hermex.test", Mailbox: dir, Exp: time.Now().Add(time.Hour).Unix()})

	req := httptest.NewRequest(http.MethodPut, "/api/v1/settings/x", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
	srv.withSettings(httptest.NewRecorder(), req, func(_ *objectstore.Store, m map[string]json.RawMessage) (any, bool) {
		m["theme"] = json.RawMessage(`"light"`)
		return map[string]string{"error": "refused"}, false
	})
	called := false
	srv.withSettings(httptest.NewRecorder(), httptest.NewRequest(http.MethodPut, "/api/v1/settings/x", nil),
		func(_ *objectstore.Store, _ map[string]json.RawMessage) (any, bool) {
			called = true
			return nil, true
		})
	if called {
		t.Error("a request without a session reached the settings writer")
	}
	blob, err := st.GetWebmailSettings()
	mustNoErr(t, "read settings", err)
	if blob != `{"theme":"dark"}` {
		t.Errorf("settings after the refused write = %s, want the seeded blob", blob)
	}
}
