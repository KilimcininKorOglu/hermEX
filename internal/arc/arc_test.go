package arc

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"
)

// TestRelaxedCanonicalizationMatchesRFC6376 runs the example of RFC 6376
// section 3.4.6 through both relaxed algorithms and the simple body one.
func TestRelaxedCanonicalizationMatchesRFC6376(t *testing.T) {
	fields, body := splitMessage([]byte("A: X\r\nB : Y\t\r\n\tZ  \r\n\r\n C \r\nD \t E\r\n\r\n\r\n"))
	var head string
	for _, f := range fields {
		head += relaxedHeader(f.raw)
	}
	if head != "a:X\r\nb:Y Z\r\n" {
		t.Errorf("relaxed header = %q", head)
	}
	if got := string(relaxedBody(body)); got != " C\r\nD E\r\n" {
		t.Errorf("relaxed body = %q", got)
	}
	if got := string(simpleBody(body)); got != " C \r\nD \t E\r\n" {
		t.Errorf("simple body = %q", got)
	}
	if got := string(simpleBody(nil)); got != "\r\n" {
		t.Errorf("simple empty body = %q, want CRLF", got)
	}
}

// keyring publishes test keys the way DNS would, under
// <selector>._domainkey.<domain>.
type keyring map[string]string

func (k keyring) lookup(name string) ([]string, error) {
	if rec, ok := k[name]; ok {
		return []string{rec}, nil
	}
	return nil, errors.New("no such record")
}

func ed25519Sealer(t *testing.T, ring keyring, domain string) Sealer {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ring["arc._domainkey."+domain] = "v=DKIM1; k=ed25519; p=" + base64.StdEncoding.EncodeToString(pub)
	return Sealer{Domain: domain, Selector: "arc", Key: priv, AuthServID: "mx." + domain, Lookup: ring.lookup}
}

func rsaSealer(t *testing.T, ring keyring, domain string) Sealer {
	t.Helper()
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKIXPublicKey(&priv.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	ring["arc._domainkey."+domain] = "v=DKIM1; k=rsa;\r\n\tp=" + base64.StdEncoding.EncodeToString(der)
	return Sealer{Domain: domain, Selector: "arc", Key: priv, AuthServID: "mx." + domain, Lookup: ring.lookup}
}

// status validates the chain a message carries, as the next hop would.
func status(raw []byte, lookup LookupTXT) string {
	fields, body := splitMessage(raw)
	sets, ok := collectSets(fields)
	return chainStatus(fields, body, sets, ok, lookup)
}

// seal seals raw with the key's own algorithm and fails the test on an error.
func seal(t *testing.T, s Sealer, raw []byte) []byte {
	t.Helper()
	out, err := s.seal(raw, time.Unix(1700000000, 0))
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// sealTag returns a tag of the topmost ARC-Seal.
func sealTag(raw []byte, tag string) string {
	fields, _ := splitMessage(raw)
	for _, f := range fields {
		if strings.EqualFold(f.name, hdrAS) {
			return tagList(fieldValue(f.raw))[tag]
		}
	}
	return ""
}

const inbound = "Authentication-Results: mx.forward.example; spf=pass smtp.mailfrom=a@origin.example;\r\n" +
	"\tdkim=pass header.d=origin.example\r\n" +
	"From: a@origin.example\r\nTo: list@forward.example\r\nSubject: hello\r\n" +
	"Date: Tue, 14 Nov 2023 22:13:20 +0000\r\nMessage-ID: <1@origin.example>\r\n\r\nBody line\r\n"

// TestSealStartsAChainThatVerifies seals a message with no chain. RFC 8617
// requires instance 1 with cv=none, an ARC-Authentication-Results carrying the
// sealer's own results, and a chain the next hop validates as pass.
func TestSealStartsAChainThatVerifies(t *testing.T) {
	ring := keyring{}
	out := seal(t, ed25519Sealer(t, ring, "forward.example"), []byte(inbound))
	if sealTag(out, "i") != "1" || sealTag(out, "cv") != cvNone {
		t.Errorf("first seal i=%s cv=%s, want i=1 cv=none", sealTag(out, "i"), sealTag(out, "cv"))
	}
	if !strings.Contains(string(out), "ARC-Authentication-Results: i=1; mx.forward.example; spf=pass smtp.mailfrom=a@origin.example; dkim=pass header.d=origin.example\r\n") {
		t.Errorf("the AAR does not carry this sealer's results:\n%s", out)
	}
	if got := status(out, ring.lookup); got != cvPass {
		t.Errorf("chain status = %s, want pass", got)
	}
	if !strings.HasSuffix(string(out), inbound) {
		t.Error("the original message was changed")
	}
}

// TestSecondHopRecordsPass adds a set on top of a valid chain with an RSA key:
// the new seal is instance 2 with cv=pass and the whole chain verifies.
func TestSecondHopRecordsPass(t *testing.T) {
	ring := keyring{}
	first := seal(t, ed25519Sealer(t, ring, "forward.example"), []byte(inbound))
	second := seal(t, rsaSealer(t, ring, "list.example"), first)
	if sealTag(second, "i") != "2" || sealTag(second, "cv") != cvPass {
		t.Errorf("second seal i=%s cv=%s, want i=2 cv=pass", sealTag(second, "i"), sealTag(second, "cv"))
	}
	if got := status(second, ring.lookup); got != cvPass {
		t.Errorf("chain status = %s, want pass", got)
	}
}

// TestBrokenChainIsRecordedAndNotExtended changes the message after the first
// seal. The next sealer must record cv=fail, and no sealer may add a set after
// a seal that says fail (RFC 8617 section 5.1.2).
func TestBrokenChainIsRecordedAndNotExtended(t *testing.T) {
	ring := keyring{}
	first := seal(t, ed25519Sealer(t, ring, "forward.example"), []byte(inbound))
	tampered := []byte(strings.Replace(string(first), "Subject: hello", "Subject: pay now", 1))
	if got := status(tampered, ring.lookup); got != cvFail {
		t.Fatalf("a changed Subject left the chain %s, want fail", got)
	}
	second := seal(t, rsaSealer(t, ring, "list.example"), tampered)
	if sealTag(second, "cv") != cvFail {
		t.Errorf("the second seal says cv=%s over a broken chain", sealTag(second, "cv"))
	}
	// Every signature on the second hop is valid, yet a chain whose newest seal
	// says fail stays fail (RFC 8617 section 5.2, step 2).
	if got := status(second, ring.lookup); got != cvFail {
		t.Errorf("a validly sealed cv=fail chain verified as %s", got)
	}
	third, err := rsaSealer(t, ring, "third.example").Seal(second, time.Now())
	if !errors.Is(err, ErrNotSealed) || string(third) != string(second) {
		t.Errorf("a chain ending in cv=fail was extended (err %v)", err)
	}
}

// TestForgedChainFails checks the chain cases a sender can forge: a body
// changed under the message signature, a seal whose key is not published, a set
// with a missing field, a repeated instance, and a first seal that claims pass.
func TestForgedChainFails(t *testing.T) {
	ring := keyring{}
	good := string(seal(t, ed25519Sealer(t, ring, "forward.example"), []byte(inbound)))
	cases := map[string]string{
		"body changed":     strings.Replace(good, "Body line", "Other line", 1),
		"duplicate set":    "ARC-Seal: i=1; cv=none; d=x; s=y; a=ed25519-sha256; b=AA==\r\n" + good,
		"missing AMS":      dropField(good, hdrAMS),
		"instance zero":    strings.Replace(good, "ARC-Seal: i=1", "ARC-Seal: i=0", 1),
		"first claims pas": strings.Replace(good, "cv=none", "cv=pass", 1),
		"results changed":  strings.Replace(good, "i=1; mx.forward.example; spf=pass", "i=1; mx.forward.example; spf=fail", 1),
	}
	for name, raw := range cases {
		if got := status([]byte(raw), ring.lookup); got != cvFail {
			t.Errorf("%s: chain status = %s, want fail", name, got)
		}
	}
	if got := status([]byte(good), keyring{}.lookup); got != cvFail {
		t.Errorf("a seal whose key is not published verified as %s", got)
	}
}

// TestSealRefusesAnEd25519Key: RFC 8617 defers more than one algorithm to a
// later document, and verifiers such as dkimpy accept only rsa-sha256 in an
// ARC set, so the server seals with RSA only and adds nothing otherwise.
func TestSealRefusesAnEd25519Key(t *testing.T) {
	out, err := ed25519Sealer(t, keyring{}, "forward.example").Seal([]byte(inbound), time.Now())
	if !errors.Is(err, ErrUnsupportedKey) || out != nil {
		t.Errorf("an Ed25519 key sealed the message (err %v)", err)
	}
}

// dropField removes the first header field with the given name.
func dropField(raw, name string) string {
	fields, body := splitMessage([]byte(raw))
	var b strings.Builder
	dropped := false
	for _, f := range fields {
		if !dropped && strings.EqualFold(f.name, name) {
			dropped = true
			continue
		}
		b.WriteString(f.raw)
	}
	return b.String() + "\r\n" + string(body)
}
