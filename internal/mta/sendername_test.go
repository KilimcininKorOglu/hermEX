package mta

import (
	"bytes"
	"fmt"
	"io"
	"net/mail"
	"testing"

	"github.com/emersion/go-msgauth/dkim"

	"hermex/internal/dkimsign"
)

// TestRewriteFromDisplayName proves the rewrite replaces only the From display name:
// the address is preserved, the Sender header and body are untouched, a folded From
// is handled, and an empty name or a message with no From is returned unchanged.
func TestRewriteFromDisplayName(t *testing.T) {
	const base = "From: Alice <alice@example.com>\r\n" +
		"Sender: relay@example.com\r\n" +
		"Subject: hi\r\n" +
		"\r\n" +
		"body\r\n"

	out := RewriteFromDisplayName([]byte(base), "Ali Veli (Acme)")
	m, err := mail.ReadMessage(bytes.NewReader(out))
	mustNoErr(t, "parse the rewritten message", err)
	wantFromAddress(t, "the rewritten From", m, "Ali Veli (Acme)", "alice@example.com")
	wantEq(t, "the Sender (untouched)", m.Header.Get("Sender"), "relay@example.com")
	body, err := io.ReadAll(m.Body)
	mustNoErr(t, "read the body", err)
	wantEq(t, "the body (untouched)", string(body), "body\r\n")

	if got := RewriteFromDisplayName([]byte(base), ""); !bytes.Equal(got, []byte(base)) {
		t.Error("empty name should return the message unchanged")
	}
	noFrom := []byte("To: bob@x.com\r\n\r\nbody\r\n")
	if got := RewriteFromDisplayName(noFrom, "X"); !bytes.Equal(got, noFrom) {
		t.Error("a message with no From should be returned unchanged")
	}

	folded := []byte("From: Alice\r\n <alice@example.com>\r\nSubject: x\r\n\r\nb\r\n")
	fm, err := mail.ReadMessage(bytes.NewReader(RewriteFromDisplayName(folded, "Bob")))
	mustNoErr(t, "parse the rewritten folded message", err)
	wantFromAddress(t, "the rewritten folded From", fm, "Bob", "alice@example.com")

	unparseable := []byte("From: not-an-address\r\n\r\nbody\r\n")
	if got := RewriteFromDisplayName(unparseable, "Bob"); !bytes.Equal(got, unparseable) {
		t.Error("unparseable From should be returned unchanged")
	}
	bodyFrom := []byte("Subject: hi\r\n\r\nFrom: Alice <alice@example.com>\r\n")
	if got := RewriteFromDisplayName(bodyFrom, "Bob"); !bytes.Equal(got, bodyFrom) {
		t.Error("From line inside body must remain untouched")
	}
}

// TestRewriteFromDisplayNameCannotAddAHeader feeds a display name carrying CR/LF and
// a header. The name comes from a domain template filled with profile fields, so a
// line break in either must stay inside the From value (RFC 5322 section 2.2 ends a
// header field at CRLF), never start a Bcc or a second From.
func TestRewriteFromDisplayNameCannotAddAHeader(t *testing.T) {
	raw := []byte("From: Alice <alice@example.com>\r\nSubject: hi\r\n\r\nbody\r\n")
	for _, name := range []string{"Eve\r\nBcc: victim@example.net", "Eve\nBcc: victim@example.net", "Eve\rBcc: victim@example.net"} {
		out := RewriteFromDisplayName(raw, name)
		m, err := mail.ReadMessage(bytes.NewReader(out))
		mustNoErr(t, "parse the rewritten message", err)
		if bcc := m.Header.Get("Bcc"); bcc != "" {
			t.Errorf("name %q added a Bcc header %q:\n%s", name, bcc, out)
		}
		wantEq(t, "header count for "+fmt.Sprintf("%q", name), len(m.Header), 2)
		from, err := mail.ParseAddress(m.Header.Get("From"))
		mustNoErr(t, "parse the From", err)
		wantEq(t, "the From address", from.Address, "alice@example.com")
	}
}

// wantFromAddress checks a message's From display name and address.
func wantFromAddress(t *testing.T, what string, m *mail.Message, name, address string) {
	t.Helper()
	from, err := mail.ParseAddress(m.Header.Get("From"))
	mustNoErr(t, "parse "+what, err)
	wantEq(t, what+" display name", from.Name, name)
	wantEq(t, what+" address", from.Address, address)
}

// senderKeys serves one domain's DKIM key for the sign/verify test.
type senderKeys struct {
	domain, selector string
	privPEM          []byte
}

func (k senderKeys) DKIMKey(domain string) ([]byte, string, bool, error) {
	if domain == k.domain {
		return k.privPEM, k.selector, true, nil
	}
	return nil, "", false, nil
}

// TestRewriteFromThenSignVerifies is the load-bearing test: rewriting the From and
// then DKIM-signing yields a signature that verifies against the rewritten From, and
// a non-ASCII (Turkish) name round-trips through the signed bytes. The rewrite touches
// the display name only, so DKIM/SPF/DMARC alignment (the domain) is unchanged.
func TestRewriteFromThenSignVerifies(t *testing.T) {
	privPEM, dnsTXT, err := dkimsign.GenerateKey(dkimsign.KeyRSA)
	if err != nil {
		t.Fatal(err)
	}
	const msg = "From: Alice <alice@example.com>\r\n" +
		"To: bob@remote.test\r\n" +
		"Subject: hi\r\n" +
		"Date: Mon, 01 Jan 2024 00:00:00 +0000\r\n" +
		"Message-ID: <m1@example.com>\r\n" +
		"\r\n" +
		"body\r\n"

	rewritten := RewriteFromDisplayName([]byte(msg), "Ali Veli (Acme - Satış)")
	s := &dkimsign.Signer{Keys: senderKeys{domain: "example.com", privPEM: privPEM, selector: "sel1"}}
	signed := s.Sign(rewritten)
	if bytes.Equal(signed, rewritten) {
		t.Fatal("message was not signed")
	}

	lookup := func(name string) ([]string, error) {
		if name == "sel1._domainkey.example.com" {
			return []string{dnsTXT}, nil
		}
		return nil, fmt.Errorf("unexpected DKIM lookup %q", name)
	}
	vs, err := dkim.VerifyWithOptions(bytes.NewReader(signed), &dkim.VerifyOptions{LookupTXT: lookup})
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if len(vs) != 1 || vs[0].Err != nil {
		t.Fatalf("DKIM did not verify the rewritten From: %+v", vs)
	}
	sm, err := mail.ReadMessage(bytes.NewReader(signed))
	mustNoErr(t, "parse signed message", err)
	wantFromAddress(t, "signed From", sm, "Ali Veli (Acme - Satış)", "alice@example.com")
}
