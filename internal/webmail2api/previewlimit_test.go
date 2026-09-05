package webmail2api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"hermex/internal/directory"
	"hermex/internal/objectstore"
)

// getAppearance reads the appearance settings the way the SPA does.
func getAppearance(t *testing.T, srv *Server, secret []byte, mbox string) map[string]json.RawMessage {
	t.Helper()
	token, err := mintToken(secret, sessionClaims{Email: "alice@hermex.test", Mailbox: mbox, Exp: time.Now().Add(time.Hour).Unix()})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/settings/appearance", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("appearance status %d: %s", rec.Code, rec.Body.String())
	}
	var out map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// TestPreviewCapReachesTheClient proves the operator's inline-preview cap travels
// all the way to the browser. The cap decides whether an attachment downloads on
// open, and that decision is made in the SPA, so a value the response never carries
// is a value nobody applies.
func TestPreviewCapReachesTheClient(t *testing.T) {
	t.Cleanup(func() { SetMaxPreviewBytes(0) })

	dir := t.TempDir()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	st.Close()
	secret := []byte("preview-cap-test-secret")
	srv := NewServer(directory.StaticAccounts{}, directory.StaticAccounts{}, nil, "mail.hermex.test", secret, "", false)

	SetMaxPreviewBytes(0) // no operator value saved: the built-in default answers
	if got := string(getAppearance(t, srv, secret, dir)["previewMaxBytes"]); got != "2097152" {
		t.Errorf("default previewMaxBytes = %s, want 2097152", got)
	}

	// The poll applies an operator edit into the running process; the very next
	// response must carry it, with no restart in between.
	SetMaxPreviewBytes(5 * 1024 * 1024)
	if got := string(getAppearance(t, srv, secret, dir)["previewMaxBytes"]); got != "5242880" {
		t.Errorf("previewMaxBytes after the operator edit = %s, want 5242880", got)
	}
}

// TestPreviewCapSurvivesASave proves a settings PUT answers with the cap too. The
// SPA takes its state from the PUT response, so omitting it there would drop the
// cap until the next full load.
func TestPreviewCapSurvivesASave(t *testing.T) {
	t.Cleanup(func() { SetMaxPreviewBytes(0) })

	dir := t.TempDir()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	st.Close()
	secret := []byte("preview-cap-save-secret")
	srv := NewServer(directory.StaticAccounts{}, directory.StaticAccounts{}, nil, "mail.hermex.test", secret, "", false)
	SetMaxPreviewBytes(3 * 1024 * 1024)

	token, err := mintToken(secret, sessionClaims{Email: "alice@hermex.test", Mailbox: dir, Exp: time.Now().Add(time.Hour).Unix()})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPut, "/api/v1/settings/appearance",
		strings.NewReader(`{"theme":"dark","filePreview":true,"previewMaxBytes":999}`))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("put status %d: %s", rec.Code, rec.Body.String())
	}
	var out map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	// The client's own 999 is ignored: the cap is the operator's, not the user's.
	if got := string(out["previewMaxBytes"]); got != "3145728" {
		t.Errorf("previewMaxBytes on the save response = %s, want 3145728", got)
	}
	if got := string(getAppearance(t, srv, secret, dir)["previewMaxBytes"]); got != "3145728" {
		t.Errorf("previewMaxBytes after the save = %s, want the operator value", got)
	}
}
