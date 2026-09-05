package webmail2api

import (
	"encoding/base64"
	"strings"
	"testing"

	"hermex/internal/directory"
)

// TestBuildOutgoingConvertsPastedPictures proves the composer's own path, not
// just the codec: a picture pasted into the editor arrives as a data: URI, and
// the message that leaves here must carry it as an inline attachment instead,
// because Outlook and Gmail do not render a data: image in received mail.
func TestBuildOutgoingConvertsPastedPictures(t *testing.T) {
	srv := NewServer(directory.StaticAccounts{}, directory.StaticAccounts{}, nil, "mail.hermex.test", []byte("send-inline-secret"), "", false)

	png := []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A, 0x00, 0x01}
	body := `<p>look</p><img src="data:image/png;base64,` + base64.StdEncoding.EncodeToString(png) + `">`
	raw, err := srv.buildOutgoing("alice@hermex.test", "alice@hermex.test", sendRequest{
		To:      []string{"bob@example.org"},
		Subject: "pasted picture",
		Body:    body,
		IsHTML:  true,
	})
	if err != nil {
		t.Fatal(err)
	}
	mime := string(raw)
	if strings.Contains(mime, "data:image/") {
		t.Errorf("the sent message still carries a data: image:\n%s", mime)
	}
	if !strings.Contains(mime, "multipart/related") {
		t.Errorf("no multipart/related, so the picture is not tied to the body:\n%s", mime)
	}
	if !strings.Contains(mime, "Content-Id: <img1.") {
		t.Errorf("no Content-ID on the picture part:\n%s", mime)
	}
	if !strings.Contains(mime, "src=\"cid:img1.") {
		t.Errorf("the body does not reference the picture part:\n%s", mime)
	}
	// The plain-text alternative is derived from the REWRITTEN body, so the
	// payload must appear exactly once, in the attachment part.
	if got := strings.Count(mime, base64.StdEncoding.EncodeToString(png)); got != 1 {
		t.Errorf("the picture payload appears %d times, want 1 (the attachment part)", got)
	}
}

// TestBuildOutgoingLeavesAPlainBodyAlone keeps the rewrite off a message that has
// no pasted picture, including a plain-text one where a data: string is just text.
func TestBuildOutgoingLeavesAPlainBodyAlone(t *testing.T) {
	srv := NewServer(directory.StaticAccounts{}, directory.StaticAccounts{}, nil, "mail.hermex.test", []byte("send-inline-secret"), "", false)

	raw, err := srv.buildOutgoing("alice@hermex.test", "alice@hermex.test", sendRequest{
		To:      []string{"bob@example.org"},
		Subject: "no pictures",
		Body:    "a plain note mentioning data:image/png;base64,AAAA verbatim",
	})
	if err != nil {
		t.Fatal(err)
	}
	mime := string(raw)
	if strings.Contains(mime, "multipart/related") {
		t.Errorf("a plain body was wrapped in a multipart/related:\n%s", mime)
	}
	if !strings.Contains(mime, "data:image/png;base64,AAAA") {
		t.Errorf("the plain body was rewritten:\n%s", mime)
	}
}
