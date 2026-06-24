package webmail2api

import (
	"archive/zip"
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"hermex/internal/directory"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// TestExportSelectedAsZip proves GET /mail/export-zip streams exactly the selected
// messages as a zip of .eml files: the right bytes land under message-<uid>.eml,
// and ids that do not resolve (unknown folder, missing uid) are skipped rather
// than corrupting the stream. An empty selection is a 400 (before any zip byte).
func TestExportSelectedAsZip(t *testing.T) {
	dir := t.TempDir()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	inbox := int64(mapi.PrivateFIDInbox)
	raw1 := []byte("From: a@b.test\r\nSubject: first\r\n\r\nbody one\r\n")
	raw2 := []byte("From: c@d.test\r\nSubject: second\r\n\r\nbody two\r\n")
	i1, err := st.AppendMessage(inbox, raw1, time.Now(), 0)
	if err != nil {
		t.Fatalf("append 1: %v", err)
	}
	i2, err := st.AppendMessage(inbox, raw2, time.Now(), 0)
	if err != nil {
		t.Fatalf("append 2: %v", err)
	}
	st.Close()

	secret := []byte("export-zip-test-secret")
	srv := NewServer(directory.StaticAccounts{}, directory.StaticAccounts{}, nil, "mail.hermex.test", secret, "", false)
	get := func(query string) *httptest.ResponseRecorder {
		token, _ := mintToken(secret, sessionClaims{Email: "alice@hermex.test", Mailbox: dir, Exp: time.Now().Add(time.Hour).Unix()})
		req := httptest.NewRequest(http.MethodGet, "/api/v1/mail/export-zip"+query, nil)
		req.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		return rec
	}

	u1 := strconv.FormatUint(uint64(i1.UID), 10)
	u2 := strconv.FormatUint(uint64(i2.UID), 10)
	// Two real messages plus a bogus folder and a missing uid: only the two reals
	// must end up in the zip.
	rec := get("?id=inbox:" + u1 + "&id=inbox:" + u2 + "&id=nope:99&id=inbox:999999")
	if rec.Code != http.StatusOK {
		t.Fatalf("export: %d %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/zip" {
		t.Errorf("Content-Type = %q, want application/zip", ct)
	}

	zr, err := zip.NewReader(bytes.NewReader(rec.Body.Bytes()), int64(rec.Body.Len()))
	if err != nil {
		t.Fatalf("read zip: %v", err)
	}
	got := map[string][]byte{}
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatalf("open entry %s: %v", f.Name, err)
		}
		b, _ := io.ReadAll(rc)
		rc.Close()
		got[f.Name] = b
	}
	if len(got) != 2 {
		t.Fatalf("zip has %d entries, want 2 (bogus ids skipped): %v", len(got), keysOf(got))
	}
	// The entries must carry each uid's actual stored message (AppendMessage
	// normalizes the MIME, so compare against the store's canonical bytes, not the
	// raw input). The body substring guards that the right uid maps to the right
	// entry, not a swap that happens to normalize alike.
	st2, err := objectstore.Open(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer st2.Close()
	want1, _ := st2.GetMessageRaw(inbox, i1.UID)
	want2, _ := st2.GetMessageRaw(inbox, i2.UID)
	if b := got["message-"+u1+".eml"]; !bytes.Equal(b, want1) || !bytes.Contains(b, []byte("body one")) {
		t.Errorf("message-%s.eml does not match stored message 1", u1)
	}
	if b := got["message-"+u2+".eml"]; !bytes.Equal(b, want2) || !bytes.Contains(b, []byte("body two")) {
		t.Errorf("message-%s.eml does not match stored message 2", u2)
	}

	// An empty selection must fail loud BEFORE any zip byte is written.
	if rec := get(""); rec.Code != http.StatusBadRequest {
		t.Errorf("empty selection: got %d, want 400", rec.Code)
	}
}

// keysOf returns the keys of m, for readable failure messages.
func keysOf(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
