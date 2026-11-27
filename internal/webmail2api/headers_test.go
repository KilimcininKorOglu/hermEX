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
	// A message that opens with the blank line has no headers (RFC 5322 section
	// 2.1), and the body after it, even one that looks like headers, is not shown.
	bodyOnly := []byte("\r\nBcc: hidden@x\r\n")
	if got := string(headerBlock(bodyOnly)); got != "" {
		t.Errorf("body-only headerBlock = %q, want empty", got)
	}
	if got := string(headerBlock(nil)); got != "" {
		t.Errorf("empty headerBlock = %q, want empty", got)
	}
}
