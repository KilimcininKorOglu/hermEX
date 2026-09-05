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
	st.Close()

	secret := []byte("settings-race-secret")
	srv := NewServer(directory.StaticAccounts{}, directory.StaticAccounts{}, nil, "mail.hermex.test", secret, "", false)
	token, _ := mintToken(secret, sessionClaims{Email: "alice@hermex.test", Mailbox: dir, Exp: time.Now().Add(time.Hour).Unix()})

	const n = 40
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			key := fmt.Sprintf("key%d", i)
			req := httptest.NewRequest(http.MethodPut, "/api/v1/settings/x", nil)
			req.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
			<-start // release all goroutines together to widen the race window
			srv.withSettings(httptest.NewRecorder(), req, func(_ *objectstore.Store, m map[string]json.RawMessage) (any, bool) {
				m[key] = json.RawMessage(`"v"`)
				return map[string]bool{"ok": true}, true
			})
		}(i)
	}
	close(start)
	wg.Wait()

	st2, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	got, _ := sharedSettings(st2)
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
