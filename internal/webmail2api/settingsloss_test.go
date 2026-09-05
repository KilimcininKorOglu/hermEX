package webmail2api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"hermex/internal/directory"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// corruptHarness seeds a mailbox whose settings blob holds several settings,
// then replaces it with something that will not parse, which is the state a
// truncated or half-written blob leaves behind.
func corruptHarness(t *testing.T) (func(method, target, body string) *httptest.ResponseRecorder, string) {
	t.Helper()
	dir := t.TempDir()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	m := map[string]json.RawMessage{
		"webmail2Favorites": json.RawMessage(`["Project X","Project Y"]`),
		"categories":        json.RawMessage(`[{"name":"Red","color":"#e7a1a2"}]`),
		"signatures":        json.RawMessage(`[{"id":"a","name":"Work","html":"<p>hi</p>"}]`),
	}
	if err := saveSharedSettings(st, m); err != nil {
		t.Fatal(err)
	}
	st.Close()

	secret := []byte("settings-loss-secret")
	accs := directory.StaticAccounts{"alice@hermex.test": {MailboxPath: dir}}
	srv := NewServer(accs, accs, nil, "mail.hermex.test", secret, "", false)
	return func(method, target, body string) *httptest.ResponseRecorder {
		token, err := mintToken(secret, sessionClaims{Email: "alice@hermex.test", Mailbox: dir, Exp: time.Now().Add(time.Hour).Unix()})
		if err != nil {
			t.Fatal(err)
		}
		var req *http.Request
		if body == "" {
			req = httptest.NewRequest(method, target, nil)
		} else {
			req = httptest.NewRequest(method, target, strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
		}
		req.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		return rec
	}, dir
}

// corruptTheBlob replaces the stored settings with text that is not JSON.
func corruptTheBlob(t *testing.T, dir string) {
	t.Helper()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	var props mapi.PropertyValues
	props.Set(mapi.PrWebmailSettings, `{"webmail2Favorites": ["Project X"`) // truncated
	if err := st.SetStoreProperties(props); err != nil {
		t.Fatal(err)
	}
}

// storedKeys lists the keys the settings blob holds, read straight from the
// store rather than through the API that is under test.
func storedKeys(t *testing.T, dir string) []string {
	t.Helper()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	props, err := st.GetStoreProperties(mapi.PrWebmailSettings)
	if err != nil {
		t.Fatal(err)
	}
	v, _ := props.Get(mapi.PrWebmailSettings)
	str, _ := v.(string)
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(str), &m); err != nil {
		return nil // still unparseable, which is what the test wants to see
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

// TestAWriteAfterAnUnreadableSettingsBlobIsRefused is the data-loss guard. A
// blob that will not parse used to read as an empty one, so the next settings
// save wrote that empty map back and the user lost every favourite, category
// and signature at once, with the save reporting success.
func TestAWriteAfterAnUnreadableSettingsBlobIsRefused(t *testing.T) {
	do, dir := corruptHarness(t)
	corruptTheBlob(t, dir)

	rec := do(http.MethodPut, "/api/v1/settings/appearance", `{"theme":"dark"}`)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("a save over an unreadable blob = %d, want 500: %s", rec.Code, rec.Body.String())
	}
	// The stored blob is untouched: still the unparseable one, not a fresh blob
	// holding only the theme.
	if keys := storedKeys(t, dir); keys != nil {
		t.Errorf("the settings were overwritten with %v", keys)
	}
}

// TestAWriteOnAReadableBlobStillSaves keeps the guard from refusing the ordinary
// case, including a mailbox that has never stored settings.
func TestAWriteOnAReadableBlobStillSaves(t *testing.T) {
	do, dir := corruptHarness(t)

	if rec := do(http.MethodPut, "/api/v1/settings/appearance", `{"theme":"dark"}`); rec.Code != http.StatusOK {
		t.Fatalf("save = %d: %s", rec.Code, rec.Body.String())
	}
	keys := storedKeys(t, dir)
	for _, want := range []string{"webmail2Favorites", "categories", "signatures"} {
		found := false
		for _, k := range keys {
			if k == want {
				found = true
			}
		}
		if !found {
			t.Errorf("%q was dropped by an ordinary save; keys are %v", want, keys)
		}
	}
}

// TestAFreshMailboxStillSaves proves an absent blob is not treated as a failed
// read. A mailbox that has never stored settings must still be able to store
// some.
func TestAFreshMailboxStillSaves(t *testing.T) {
	dir := t.TempDir()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	st.Close()

	secret := []byte("fresh-mailbox-secret")
	accs := directory.StaticAccounts{"alice@hermex.test": {MailboxPath: dir}}
	srv := NewServer(accs, accs, nil, "mail.hermex.test", secret, "", false)
	token, err := mintToken(secret, sessionClaims{Email: "alice@hermex.test", Mailbox: dir, Exp: time.Now().Add(time.Hour).Unix()})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPut, "/api/v1/settings/appearance", strings.NewReader(`{"theme":"dark"}`))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("a fresh mailbox could not save: %d %s", rec.Code, rec.Body.String())
	}
}
