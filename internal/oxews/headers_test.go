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
