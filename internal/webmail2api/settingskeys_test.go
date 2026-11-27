package webmail2api

import (
	"encoding/json"
	"net/http"
	"testing"

	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// storeSettingsKey writes one raw value under key in the mailbox's settings blob,
// keeping the other keys, the state a half-written or foreign value leaves.
func storeSettingsKey(t *testing.T, dir, key, raw string) {
	t.Helper()
	st, err := objectstore.Open(dir)
	mustNoErr(t, "open the mailbox", err)
	defer mustClose(t, st)
	m, loaded := sharedSettings(st)
	if !loaded {
		t.Fatal("the seeded settings blob does not read")
	}
	m[key] = json.RawMessage(raw)
	mustNoErr(t, "store the settings", saveSharedSettings(st, m))
}

// storedSettingsKey reads the raw value under key straight from the store, not
// through the API under test.
func storedSettingsKey(t *testing.T, dir, key string) string {
	t.Helper()
	st, err := objectstore.Open(dir)
	mustNoErr(t, "open the mailbox", err)
	defer mustClose(t, st)
	props, err := st.GetStoreProperties(mapi.PrWebmailSettings)
	mustNoErr(t, "read the store properties", err)
	v, found := props.Get(mapi.PrWebmailSettings)
	if !found {
		t.Fatal("the settings blob is gone")
	}
	str, isStr := v.(string)
	if !isStr {
		t.Fatalf("the settings blob is %T", v)
	}
	var m map[string]json.RawMessage
	mustNoErr(t, "decode the settings blob", json.Unmarshal([]byte(str), &m))
	return string(m[key])
}

// TestAnEditOverAnUndecodableSettingIsRefused is the per-key data-loss guard. A
// list that did not decode used to read as an empty one, so adding one signature,
// template, filter or pin saved a list holding only that one and the user lost the
// rest, with the edit reporting success. Each edit is refused and the stored value
// is left exactly as it was.
func TestAnEditOverAnUndecodableSettingIsRefused(t *testing.T) {
	// A value of the wrong JSON type is what a different client, or an older
	// shape of the same key, leaves behind.
	const foreign = `{"not":"a list"}`
	cases := []struct {
		key, method, path, body string
	}{
		{"signatures", http.MethodPost, "/api/v1/signatures", `{"name":"New","body":"<p>x</p>"}`},
		{"signatures", http.MethodDelete, "/api/v1/signatures?name=Work", ""},
		{"webmail2Templates", http.MethodPost, "/api/v1/templates", `{"name":"T","subject":"s","body":"b"}`},
		{"webmail2Filters", http.MethodPost, "/api/v1/filters", `{"name":"F","enabled":false,"conditions":[],"actions":[]}`},
		{"webmail2FavoriteFolders", http.MethodPost, "/api/v1/favorites/toggle", `{"id":"inbox"}`},
	}
	for _, tc := range cases {
		t.Run(tc.method+" "+tc.key, func(t *testing.T) {
			do, dir := corruptHarness(t)
			storeSettingsKey(t, dir, tc.key, foreign)

			rec := do(tc.method, tc.path, tc.body)
			wantStatus(t, "edit over an undecodable "+tc.key, rec, http.StatusInternalServerError)
			wantEq(t, "stored "+tc.key, storedSettingsKey(t, dir, tc.key), foreign)
		})
	}
}

// TestAReadOfAnUndecodableListIsAnError keeps a list read from answering a
// damaged list as an empty one, which the user would read as "all gone".
func TestAReadOfAnUndecodableListIsAnError(t *testing.T) {
	for _, tc := range []struct{ key, path string }{
		{"signatures", "/api/v1/signatures"},
		{"webmail2Templates", "/api/v1/templates"},
		{"webmail2Filters", "/api/v1/filters"},
		{"webmail2FavoriteFolders", "/api/v1/favorites"},
	} {
		t.Run(tc.key, func(t *testing.T) {
			do, dir := corruptHarness(t)
			storeSettingsKey(t, dir, tc.key, `"text"`)
			wantStatus(t, "read of an undecodable "+tc.key, do(http.MethodGet, tc.path, ""), http.StatusInternalServerError)
		})
	}
}

// TestAnEditOverADecodableListStillSaves keeps the guard from refusing the
// ordinary edit: the new signature joins the stored one.
func TestAnEditOverADecodableListStillSaves(t *testing.T) {
	do, dir := corruptHarness(t)
	wantStatus(t, "add a signature", do(http.MethodPost, "/api/v1/signatures", `{"name":"New","body":"<p>x</p>"}`), http.StatusOK)
	var sigs []webmailSig
	mustNoErr(t, "decode the stored signatures", json.Unmarshal([]byte(storedSettingsKey(t, dir, "signatures")), &sigs))
	wantEq(t, "stored signatures", len(sigs), 2)
	wantEq(t, "kept signature", sigs[0].Name, "Work")
	wantEq(t, "added signature", sigs[1].Name, "New")
}
