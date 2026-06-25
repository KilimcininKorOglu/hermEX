package webmail2api

import "testing"

// TestHeaderBlock proves the internet-headers view returns exactly the header
// section: everything up to the blank line that separates headers from the body,
// for both CRLF and bare-LF messages, and the whole message when there is no body.
func TestHeaderBlock(t *testing.T) {
	crlf := []byte("From: a@x\r\nTo: b@y\r\nSubject: hi\r\n\r\nbody line\r\n")
	if got, want := string(headerBlock(crlf)), "From: a@x\r\nTo: b@y\r\nSubject: hi\r\n"; got != want {
		t.Errorf("CRLF headerBlock = %q, want %q", got, want)
	}
	lf := []byte("From: a@x\nSubject: hi\n\nbody\n")
	if got, want := string(headerBlock(lf)), "From: a@x\nSubject: hi\n"; got != want {
		t.Errorf("LF headerBlock = %q, want %q", got, want)
	}
	noBlank := []byte("From: a@x\r\nSubject: hi\r\n")
	if got := string(headerBlock(noBlank)); got != string(noBlank) {
		t.Errorf("no-blank headerBlock = %q, want the whole message", got)
	}
}
