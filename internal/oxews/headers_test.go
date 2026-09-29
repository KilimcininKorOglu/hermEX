package oxews

import (
	"encoding/xml"
	"strings"
	"testing"

	"hermex/internal/mapi"
)

// TestEmptyHeaderNameIsWritten proves a header field with no name keeps its
// HeaderName attribute, empty, since the schema requires it and a client reads it
// without testing for absence.
func TestEmptyHeaderNameIsWritten(t *testing.T) {
	var props mapi.PropertyValues
	props.Set(mapi.PrTransportMessageHeaders, "Subject: x\r\n: orphan\r\n\r\n")
	out, err := xml.Marshal(MessageHeaders(props))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), `<InternetMessageHeader HeaderName="">orphan</InternetMessageHeader>`) {
		t.Errorf("the nameless field lost its HeaderName: %s", out)
	}
	if MessageHeaders(nil) != nil {
		t.Error("an item without a header block got an empty InternetMessageHeaders")
	}
}
