package ews

import (
	"encoding/xml"
	"testing"

	"hermex/internal/oxews"
)

func convertIDBody(destFormat, srcFormat, id, mailbox string) string {
	return `<ConvertId xmlns="` + nsMessages + `" xmlns:t="` + nsTypes + `" DestinationFormat="` + destFormat + `">` +
		`<SourceIds><t:AlternateId Format="` + srcFormat + `" Id="` + id + `" Mailbox="` + mailbox + `"/></SourceIds>` +
		`</ConvertId>`
}

type parsedConvertID struct {
	Msgs []struct {
		Class string `xml:"ResponseClass,attr"`
		Code  string `xml:"ResponseCode"`
		Alt   struct {
			Format  string `xml:"Format,attr"`
			ID      string `xml:"Id,attr"`
			Mailbox string `xml:"Mailbox,attr"`
		} `xml:"AlternateId"`
	} `xml:"Body>ConvertIdResponse>ResponseMessages>ConvertIdResponseMessage"`
}

func mustParseConvertID(t *testing.T, body string) parsedConvertID {
	t.Helper()
	var p parsedConvertID
	if err := xml.Unmarshal([]byte(body), &p); err != nil {
		t.Fatalf("parse ConvertId: %v\n%s", err, body)
	}
	return p
}

// TestConvertIdEwsIdentity proves the supported EwsId-to-EwsId conversion echoes a
// valid item id unchanged, with its mailbox preserved.
func TestConvertIdEwsIdentity(t *testing.T) {
	ts, _ := seededEWS(t)
	id := oxews.EncodeItemID(oxews.ItemID{FolderID: 0x0d, MessageID: 42, UID: 7})

	_, body := soapPost(t, ts, wrapRequest(convertIDBody("EwsId", "EwsId", id, "user@hermex.test")), true)
	p := mustParseConvertID(t, body)
	if len(p.Msgs) != 1 {
		t.Fatalf("got %d response messages, want 1\n%s", len(p.Msgs), body)
	}
	m := p.Msgs[0]
	if m.Class != "Success" || m.Code != "NoError" {
		t.Fatalf("class/code = %q/%q, want Success/NoError\n%s", m.Class, m.Code, body)
	}
	if m.Alt.ID != id {
		t.Errorf("converted id = %q, want the source id %q (identity)", m.Alt.ID, id)
	}
	if m.Alt.Format != "EwsId" {
		t.Errorf("converted format = %q, want EwsId", m.Alt.Format)
	}
	if m.Alt.Mailbox != "user@hermex.test" {
		t.Errorf("converted mailbox = %q, want it preserved", m.Alt.Mailbox)
	}
}

// TestConvertIdFolderIdentity proves a folder EwsId also round-trips through the
// identity conversion.
func TestConvertIdFolderIdentity(t *testing.T) {
	ts, _ := seededEWS(t)
	id := oxews.EncodeFolderID(0x1e)

	_, body := soapPost(t, ts, wrapRequest(convertIDBody("EwsId", "EwsId", id, "user@hermex.test")), true)
	p := mustParseConvertID(t, body)
	if len(p.Msgs) != 1 || p.Msgs[0].Class != "Success" || p.Msgs[0].Alt.ID != id {
		t.Fatalf("folder id identity failed: %s", body)
	}
}

// TestConvertIdUnsupportedDestination proves converting to a format hermEX cannot
// mint (here OwaId) is refused with ErrorUnsupportedTypeForConversion, never a
// fabricated id.
func TestConvertIdUnsupportedDestination(t *testing.T) {
	ts, _ := seededEWS(t)
	id := oxews.EncodeItemID(oxews.ItemID{FolderID: 0x0d, MessageID: 1, UID: 1})

	_, body := soapPost(t, ts, wrapRequest(convertIDBody("OwaId", "EwsId", id, "user@hermex.test")), true)
	p := mustParseConvertID(t, body)
	if len(p.Msgs) != 1 || p.Msgs[0].Class != "Error" || p.Msgs[0].Code != "ErrorUnsupportedTypeForConversion" {
		t.Fatalf("unsupported destination: want Error/ErrorUnsupportedTypeForConversion, got %s", body)
	}
	if p.Msgs[0].Alt.ID != "" {
		t.Errorf("an unsupported conversion must not return an id, got %q", p.Msgs[0].Alt.ID)
	}
}

// TestConvertIdUnsupportedSource proves a source in a format hermEX does not read
// (here EntryId) is refused with ErrorUnsupportedTypeForConversion.
func TestConvertIdUnsupportedSource(t *testing.T) {
	ts, _ := seededEWS(t)

	_, body := soapPost(t, ts, wrapRequest(convertIDBody("EwsId", "EntryId", "0102deadbeef", "user@hermex.test")), true)
	p := mustParseConvertID(t, body)
	if len(p.Msgs) != 1 || p.Msgs[0].Code != "ErrorUnsupportedTypeForConversion" {
		t.Fatalf("unsupported source: want ErrorUnsupportedTypeForConversion, got %s", body)
	}
}

// TestConvertIdMalformed proves a source that claims to be an EwsId but does not
// decode is ErrorInvalidIdMalformed.
func TestConvertIdMalformed(t *testing.T) {
	ts, _ := seededEWS(t)

	_, body := soapPost(t, ts, wrapRequest(convertIDBody("EwsId", "EwsId", "!!!not-a-token!!!", "user@hermex.test")), true)
	p := mustParseConvertID(t, body)
	if len(p.Msgs) != 1 || p.Msgs[0].Code != "ErrorInvalidIdMalformed" {
		t.Fatalf("malformed id: want ErrorInvalidIdMalformed, got %s", body)
	}
}

// TestConvertIdMultiple proves a request with several source ids returns one
// response message per source, in order.
func TestConvertIdMultiple(t *testing.T) {
	ts, _ := seededEWS(t)
	good := oxews.EncodeItemID(oxews.ItemID{FolderID: 0x0d, MessageID: 5, UID: 2})
	body := `<ConvertId xmlns="` + nsMessages + `" xmlns:t="` + nsTypes + `" DestinationFormat="EwsId">` +
		`<SourceIds>` +
		`<t:AlternateId Format="EwsId" Id="` + good + `" Mailbox="a@hermex.test"/>` +
		`<t:AlternateId Format="EwsId" Id="garbage" Mailbox="b@hermex.test"/>` +
		`</SourceIds></ConvertId>`
	_, resp := soapPost(t, ts, wrapRequest(body), true)
	p := mustParseConvertID(t, resp)
	if len(p.Msgs) != 2 {
		t.Fatalf("got %d response messages, want 2\n%s", len(p.Msgs), resp)
	}
	if p.Msgs[0].Code != "NoError" {
		t.Errorf("first (valid) id code = %q, want NoError", p.Msgs[0].Code)
	}
	if p.Msgs[1].Code != "ErrorInvalidIdMalformed" {
		t.Errorf("second (garbage) id code = %q, want ErrorInvalidIdMalformed", p.Msgs[1].Code)
	}
}
