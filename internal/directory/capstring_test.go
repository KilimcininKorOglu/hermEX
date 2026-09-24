package directory

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// TestCapStringKeepsWholeCharacters holds capString to a character boundary. A
// column's byte cap that falls inside a multi-byte character leaves an invalid
// UTF-8 tail, and MariaDB refuses the whole row for it.
func TestCapStringKeepsWholeCharacters(t *testing.T) {
	s := strings.Repeat("a", 254) + "ş" // "ş" is two bytes, at bytes 254 and 255
	got := capString(s, 255)
	if !utf8.ValidString(got) {
		t.Fatalf("capString left invalid UTF-8: %q", got[250:])
	}
	wantEq(t, "the capped length", len(got), 254)
	wantEq(t, "a short string", capString("şş", 10), "şş")
}

// TestAQuarantineSubjectCutInsideACharacterIsStored is the failure the helper
// caused: the subject column is capped at 255 bytes, and a subject whose 255th
// byte falls inside a character made the insert fail.
func TestAQuarantineSubjectCutInsideACharacterIsStored(t *testing.T) {
	d, _ := freshDirectory(t)
	dom := mustCreateDomain(t, d, t.TempDir(), "acme.test")
	_, err := d.QuarantineMessage(QuarantineEntry{
		Direction: "inbound", MailFrom: "a@b.example", Recipients: []string{"x@acme.test"},
		Subject: strings.Repeat("a", 254) + "şşş", VirusName: "Eicar", DomainID: dom, CreatedAt: 1,
	})
	mustNoErr(t, "quarantine a message with a long non-ASCII subject", err)
}
