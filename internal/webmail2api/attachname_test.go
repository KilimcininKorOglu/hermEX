package webmail2api

import (
	"archive/zip"
	"bytes"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"hermex/internal/directory"
	"hermex/internal/mapi"
	"hermex/internal/objectstore"
)

// TestSafeAttachmentNameStripsTraversal proves a sender cannot choose where an
// extracted file lands. The archive entry name is the sender's filename verbatim
// otherwise, and any extraction tool that honours relative paths writes outside
// the target directory: the attacker needs only to send the victim an email.
func TestSafeAttachmentNameStripsTraversal(t *testing.T) {
	cases := map[string]string{
		"../../../.ssh/authorized_keys":       "authorized_keys",
		"..\\..\\Windows\\System32\\evil.dll": "evil.dll",
		"/etc/passwd":                         "passwd",
		"foo/bar/report.pdf":                  "report.pdf",
	}
	for raw, want := range cases {
		t.Run(raw, func(t *testing.T) {
			got := safeAttachmentName(raw, "fallback")
			if got != want {
				t.Errorf("safeAttachmentName(%q) = %q, want %q", raw, got, want)
			}
			if strings.ContainsAny(got, `/\`) {
				t.Errorf("result %q still carries a path separator", got)
			}
		})
	}
}

// TestSafeAttachmentNameFallsBack proves a name that reduces to nothing, or to a
// dot segment, yields the caller's generated name rather than an empty or
// traversal-shaped entry.
func TestSafeAttachmentNameFallsBack(t *testing.T) {
	for _, raw := range []string{"", ".", "..", "/", "../", "\x00\x01", "   "} {
		if got := safeAttachmentName(raw, "attachment-3"); got != "attachment-3" {
			t.Errorf("safeAttachmentName(%q) = %q, want the fallback", raw, got)
		}
	}
}

// TestSafeAttachmentNameNeutralizesHeaderBreakers proves a quote cannot end the
// Content-Disposition parameter early and hand the sender the rest of the header,
// and that control characters do not survive into it.
func TestSafeAttachmentNameNeutralizesHeaderBreakers(t *testing.T) {
	got := safeAttachmentName("a\"; filename=\"owned.exe", "fallback")
	if strings.Contains(got, `"`) {
		t.Errorf("result %q still carries a quote", got)
	}
	if got := safeAttachmentName("re\rport\n.pdf", "fallback"); strings.ContainsAny(got, "\r\n") {
		t.Errorf("result %q still carries a line break", got)
	}
}

// TestSafeAttachmentNameKeepsOrdinaryNames is the control: the sanitizer must not
// mangle the names attachments actually carry, including non-ASCII ones, or every
// download would come back renamed.
func TestSafeAttachmentNameKeepsOrdinaryNames(t *testing.T) {
	for _, name := range []string{
		"report.pdf", "Q3 results (final).xlsx", "faturalar-ağustos.pdf", "photo.2024-01-02.jpeg",
	} {
		if got := safeAttachmentName(name, "fallback"); got != name {
			t.Errorf("safeAttachmentName(%q) = %q, want it unchanged", name, got)
		}
	}
}

// TestSafeAttachmentNameBoundsLength proves an absurdly long sender-supplied name
// is truncated rather than written whole into an archive index.
func TestSafeAttachmentNameBoundsLength(t *testing.T) {
	got := safeAttachmentName(strings.Repeat("a", 5000)+".pdf", "fallback")
	if len(got) > 200 {
		t.Errorf("result is %d bytes, want it bounded", len(got))
	}
}

// TestAttachmentsZipRefusesSenderPaths drives the real endpoint with a message
// whose attachment filename is a traversal path, the way an external sender
// crafts it, and proves the archive entry cannot escape the extraction directory.
func TestAttachmentsZipRefusesSenderPaths(t *testing.T) {
	dir := t.TempDir()
	st, err := objectstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	raw := []byte("From: attacker@evil.test\r\n" +
		"Subject: invoice\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: multipart/mixed; boundary=\"bnd\"\r\n\r\n" +
		"--bnd\r\nContent-Type: text/plain\r\n\r\nsee attached\r\n" +
		"--bnd\r\nContent-Type: application/octet-stream\r\n" +
		"Content-Disposition: attachment; filename=\"../../../.ssh/authorized_keys\"\r\n\r\n" +
		"ssh-rsa AAAA attacker\r\n" +
		"--bnd--\r\n")
	info, err := st.AppendMessage(int64(mapi.PrivateFIDInbox), raw, time.Now(), 0)
	if err != nil {
		t.Fatal(err)
	}
	st.Close()

	secret := []byte("attach-name-test-secret")
	srv := NewServer(directory.StaticAccounts{}, directory.StaticAccounts{}, nil, "mail.hermex.test", secret, "", false)
	token, err := mintToken(secret, sessionClaims{
		Email: "alice@hermex.test", Mailbox: dir, Exp: time.Now().Add(time.Hour).Unix(),
	})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet,
		"/api/v1/mail/attachments-zip?id=inbox:"+strconv.FormatUint(uint64(info.UID), 10), nil)
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("attachments-zip: %d %s", rec.Code, rec.Body.String())
	}

	zr, err := zip.NewReader(bytes.NewReader(rec.Body.Bytes()), int64(rec.Body.Len()))
	if err != nil {
		t.Fatalf("read zip: %v", err)
	}
	if len(zr.File) != 1 {
		t.Fatalf("archive holds %d entries, want 1", len(zr.File))
	}
	name := zr.File[0].Name
	if strings.ContainsAny(name, `/\`) || strings.Contains(name, "..") {
		t.Errorf("archive entry %q escapes the extraction directory", name)
	}
	if name != "authorized_keys" {
		t.Errorf("archive entry = %q, want the base name", name)
	}
}
