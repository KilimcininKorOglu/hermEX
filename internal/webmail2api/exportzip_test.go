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
	mustNoErr(t, "open mailbox", err)
	inbox := int64(mapi.PrivateFIDInbox)
	i1, err := st.AppendMessage(inbox, []byte("From: a@b.test\r\nSubject: first\r\n\r\nbody one\r\n"), time.Now(), 0)
	mustNoErr(t, "append 1", err)
	i2, err := st.AppendMessage(inbox, []byte("From: c@d.test\r\nSubject: second\r\n\r\nbody two\r\n"), time.Now(), 0)
	mustNoErr(t, "append 2", err)
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
	wantStatus(t, "export", rec, http.StatusOK)
	wantEq(t, "Content-Type", rec.Header().Get("Content-Type"), "application/zip")

	got := zipEntries(t, rec)
	if len(got) != 2 {
		t.Fatalf("zip has %d entries, want 2 (bogus ids skipped): %v", len(got), keysOf(got))
	}
	// The entries must carry each uid's actual stored message (AppendMessage
	// normalizes the MIME, so compare against the store's canonical bytes, not the
	// raw input). The body substring guards that the right uid maps to the right
	// entry, not a swap that happens to normalize alike.
	st2, err := objectstore.Open(dir)
	mustNoErr(t, "reopen mailbox", err)
	defer st2.Close()
	want1, _ := st2.GetMessageRaw(inbox, i1.UID)
	want2, _ := st2.GetMessageRaw(inbox, i2.UID)
	wantZipEntry(t, got, "message-"+u1+".eml", want1, "body one")
	wantZipEntry(t, got, "message-"+u2+".eml", want2, "body two")

	// An empty selection must fail loud BEFORE any zip byte is written.
	wantStatus(t, "empty selection", get(""), http.StatusBadRequest)
}

// zipEntries reads a zip response into its entries, keyed by name.
func zipEntries(t *testing.T, rec *httptest.ResponseRecorder) map[string][]byte {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(rec.Body.Bytes()), int64(rec.Body.Len()))
	mustNoErr(t, "read zip", err)
	out := map[string][]byte{}
	for _, f := range zr.File {
		rc, err := f.Open()
		mustNoErr(t, "open entry "+f.Name, err)
		b, _ := io.ReadAll(rc)
		rc.Close()
		out[f.Name] = b
	}
	return out
}

// wantZipEntry checks one entry carries exactly the stored message, and that the
// body substring belongs to it.
func wantZipEntry(t *testing.T, entries map[string][]byte, name string, want []byte, bodyMark string) {
	t.Helper()
	got := entries[name]
	if !bytes.Equal(got, want) || !bytes.Contains(got, []byte(bodyMark)) {
		t.Errorf("%s does not match the stored message carrying %q", name, bodyMark)
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
