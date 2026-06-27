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

// TestConvertIdSameFormatNonEws proves a same-format conversion echoes the id for
// any format, not only EwsId (a same-format request is a passthrough).
func TestConvertIdSameFormatNonEws(t *testing.T) {
	ts, _ := seededEWS(t)

	_, body := soapPost(t, ts, wrapRequest(convertIDBody("EntryId", "EntryId", "0102deadbeef", "user@hermex.test")), true)
	p := mustParseConvertID(t, body)
	if len(p.Msgs) != 1 || p.Msgs[0].Class != "Success" || p.Msgs[0].Alt.ID != "0102deadbeef" {
		t.Fatalf("same-format EntryId echo failed: %s", body)
	}
}

// TestConvertIdCrossFormat proves converting from one format to a different one is
// refused with ErrorUnsupportedTypeForConversion, never a fabricated id.
func TestConvertIdCrossFormat(t *testing.T) {
	ts, _ := seededEWS(t)
	id := oxews.EncodeItemID(oxews.ItemID{FolderID: 0x0d, MessageID: 1, UID: 1})

	_, body := soapPost(t, ts, wrapRequest(convertIDBody("EntryId", "EwsId", id, "user@hermex.test")), true)
	p := mustParseConvertID(t, body)
	if len(p.Msgs) != 1 || p.Msgs[0].Class != "Error" || p.Msgs[0].Code != "ErrorUnsupportedTypeForConversion" {
		t.Fatalf("cross-format: want Error/ErrorUnsupportedTypeForConversion, got %s", body)
	}
	if p.Msgs[0].Alt.ID != "" {
		t.Errorf("an unsupported conversion must not return an id, got %q", p.Msgs[0].Alt.ID)
	}
}

// TestConvertIdMultiple proves a request with several source ids returns one
// response message per source, in order: a same-format echo and a cross-format
// refusal.
func TestConvertIdMultiple(t *testing.T) {
	ts, _ := seededEWS(t)
	good := oxews.EncodeItemID(oxews.ItemID{FolderID: 0x0d, MessageID: 5, UID: 2})
	body := `<ConvertId xmlns="` + nsMessages + `" xmlns:t="` + nsTypes + `" DestinationFormat="EwsId">` +
		`<SourceIds>` +
		`<t:AlternateId Format="EwsId" Id="` + good + `" Mailbox="a@hermex.test"/>` +
		`<t:AlternateId Format="OwaId" Id="someowaid" Mailbox="b@hermex.test"/>` +
		`</SourceIds></ConvertId>`
	_, resp := soapPost(t, ts, wrapRequest(body), true)
	p := mustParseConvertID(t, resp)
	if len(p.Msgs) != 2 {
		t.Fatalf("got %d response messages, want 2\n%s", len(p.Msgs), resp)
	}
	if p.Msgs[0].Code != "NoError" {
		t.Errorf("first (same-format) id code = %q, want NoError", p.Msgs[0].Code)
	}
	if p.Msgs[1].Code != "ErrorUnsupportedTypeForConversion" {
		t.Errorf("second (cross-format) id code = %q, want ErrorUnsupportedTypeForConversion", p.Msgs[1].Code)
	}
}
