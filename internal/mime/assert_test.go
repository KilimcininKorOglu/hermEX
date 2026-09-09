package mime

import (
	"testing"
)

// This file is the package's assertion vocabulary. A test states one fact per call,
// so a failure names the fact that broke rather than the condition that evaluated.

// wantEq fails the test unless the value equals what the caller expected.
func wantEq[T comparable](t *testing.T, got, want T, what string) {
	t.Helper()
	if got != want {
		t.Errorf("%s = %v, want %v", what, got, want)
	}
}

// mustNoErr stops the test on an error from a parser under test.
func mustNoErr(t *testing.T, err error, what string) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s: %v", what, err)
	}
}

// wantAddr fails the test unless an envelope address carries the expected name
// and mailbox@host.
func wantAddr(t *testing.T, got Address, name, mailbox, host, what string) {
	t.Helper()
	wantEq(t, got.Name, name, what+" name")
	wantEq(t, got.Mailbox, mailbox, what+" mailbox")
	wantEq(t, got.Host, host, what+" host")
}

// wantPartType fails the test unless a part carries the expected media type.
func wantPartType(t *testing.T, p *Part, typ, subtype, what string) {
	t.Helper()
	wantEq(t, p.Type+"/"+p.Subtype, typ+"/"+subtype, what)
}
