package oxews

import (
	"testing"

	"hermex/internal/mapi"
)

// TestMessageHeadersFields proves a nameless ": value" line is skipped, since
// HeaderName has nothing to carry for it, and an encoded-word value reaches the
// client as the text it encodes.
func TestMessageHeadersFields(t *testing.T) {
	var props mapi.PropertyValues
	props.Set(mapi.PrTransportMessageHeaders, "Subject: =?utf-8?Q?G=C3=BCnayd=C4=B1n?=\r\n: orphan\r\nX-Plain: a\r\n\r\n")
	got := MessageHeaders(props)
	want := []InternetHeader{{Name: "Subject", Value: "Günaydın"}, {Name: "X-Plain", Value: "a"}}
	if got == nil || len(got.Headers) != len(want) {
		t.Fatalf("headers %+v, want %+v", got, want)
	}
	for i, h := range want {
		if got.Headers[i] != h {
			t.Errorf("header %d = %+v, want %+v", i, got.Headers[i], h)
		}
	}
	if MessageHeaders(nil) != nil {
		t.Error("an item without a header block got an empty InternetMessageHeaders")
	}
}

// TestMessageHeadersSkipsALineWithoutAColon stores a header block a sender broke
// with a line that has no colon. RFC 5322 2.2 makes a header field a name, a
// colon and a body, so that line is no field and must not reach the client as a
// header named after the whole line; the fields around it still do.
func TestMessageHeadersSkipsALineWithoutAColon(t *testing.T) {
	var props mapi.PropertyValues
	props.Set(mapi.PrTransportMessageHeaders, "X-Before: 1\r\nnot a header field\r\nX-After: 2\r\n\r\n")
	got := MessageHeaders(props)
	want := []InternetHeader{{Name: "X-Before", Value: "1"}, {Name: "X-After", Value: "2"}}
	if got == nil || len(got.Headers) != len(want) {
		t.Fatalf("headers %+v, want %+v", got, want)
	}
	for i, h := range want {
		wantEq(t, got.Headers[i], h, "header")
	}
}

// TestMessageHeadersIgnoresANonStringBlock stores the header property with a
// value of the wrong type, which only a broken writer produces; the item must be
// served without headers rather than with a block read from nothing.
func TestMessageHeadersIgnoresANonStringBlock(t *testing.T) {
	props := mapi.PropertyValues{{Tag: mapi.PrTransportMessageHeaders, Value: []byte("Subject: x\r\n\r\n")}}
	if got := MessageHeaders(props); got != nil {
		t.Errorf("headers %+v, want none for a non-string block", got)
	}
}
