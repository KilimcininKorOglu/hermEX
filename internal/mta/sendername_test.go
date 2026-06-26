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
	if err != nil {
		t.Fatal(err)
	}
	if from, _ := mail.ParseAddress(m.Header.Get("From")); from == nil ||
		from.Name != "Ali Veli (Acme)" || from.Address != "alice@example.com" {
		t.Errorf("From = %+v, want the rewritten name with the original address", from)
	}
	if m.Header.Get("Sender") != "relay@example.com" {
		t.Errorf("Sender = %q, want untouched", m.Header.Get("Sender"))
	}
	if body, _ := io.ReadAll(m.Body); string(body) != "body\r\n" {
		t.Errorf("body = %q, want untouched", body)
	}

	if got := RewriteFromDisplayName([]byte(base), ""); !bytes.Equal(got, []byte(base)) {
		t.Error("empty name should return the message unchanged")
	}
	noFrom := []byte("To: bob@x.com\r\n\r\nbody\r\n")
	if got := RewriteFromDisplayName(noFrom, "X"); !bytes.Equal(got, noFrom) {
		t.Error("a message with no From should be returned unchanged")
	}

	folded := []byte("From: Alice\r\n <alice@example.com>\r\nSubject: x\r\n\r\nb\r\n")
	fm, _ := mail.ReadMessage(bytes.NewReader(RewriteFromDisplayName(folded, "Bob")))
	if fa, _ := mail.ParseAddress(fm.Header.Get("From")); fa == nil ||
		fa.Address != "alice@example.com" || fa.Name != "Bob" {
		t.Errorf("folded From rewrite = %+v, want Bob with the original address", fa)
	}
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
	privPEM, dnsTXT, err := dkimsign.GenerateKey()
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
	sm, _ := mail.ReadMessage(bytes.NewReader(signed))
	if from, _ := mail.ParseAddress(sm.Header.Get("From")); from == nil ||
		from.Name != "Ali Veli (Acme - Satış)" || from.Address != "alice@example.com" {
		t.Errorf("signed From = %+v, want the rewritten Turkish name with the original address", from)
	}
}
