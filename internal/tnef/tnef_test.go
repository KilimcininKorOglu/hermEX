package tnef_test

import (
	"errors"
	"testing"

	"hermex/internal/mapi"
	"hermex/internal/tnef"
	"hermex/internal/tnef/tneftest"
)

// propValue returns the value a decoded list holds for tag.
func propValue(props []tnef.Prop, tag mapi.PropTag) any {
	for _, p := range props {
		if p.Tag == tag && p.Name == nil {
			return p.Value
		}
	}
	return nil
}

// namedValue returns the value a decoded list holds for a named property.
func namedValue(props []tnef.Prop, name mapi.PropertyName) any {
	for _, p := range props {
		if p.Name != nil && *p.Name == name {
			return p.Value
		}
	}
	return nil
}

// TestDecodeMessageAndAttachment proves a stream's message attributes, its
// encapsulated properties (a named one included) and one attachment decode into
// the properties they stand for.
func TestDecodeMessageAndAttachment(t *testing.T) {
	name := mapi.PropertyName{Kind: mapi.MnidString, GUID: mapi.PsPublicStrings, Name: "Keywords-x"}
	props := (&tneftest.Props{}).
		Unicode(mapi.PrBody, "Grüße").
		NamedUnicode(name, "tagged")
	att := (&tneftest.Props{}).Long(mapi.PrAttachMethod, int32(mapi.AttachByValue))
	s := tneftest.New().
		Attr(1, tneftest.AttMessageClass, tneftest.CString("IPM.Microsoft Mail.Note")).
		Attr(1, tneftest.AttSubject, tneftest.CString("Caf\xe9")).
		Attr(1, tneftest.AttMsgProps, props.Bytes()).
		Attr(2, tneftest.AttAttachRendData, tneftest.RendData()).
		Attr(2, tneftest.AttAttachTitle, tneftest.CString("report.pdf")).
		Attr(2, tneftest.AttAttachData, []byte("%PDF-1.4")).
		Attr(2, tneftest.AttAttachment, att.Bytes())

	m, err := tnef.Decode(s.Bytes(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if v := propValue(m.Props, mapi.PrMessageClass); v != "IPM.Note" {
		t.Errorf("class = %v, want the legacy name mapped to IPM.Note", v)
	}
	if v := propValue(m.Props, mapi.PrSubject); v != "Café" {
		t.Errorf("subject = %q, want the 8-bit text decoded", v)
	}
	if v := propValue(m.Props, mapi.PrBody); v != "Grüße" {
		t.Errorf("body = %q", v)
	}
	if named := namedValue(m.Props, name); named != "tagged" {
		t.Errorf("named property = %v, want it decoded with its name", named)
	}
	checkOneAttachment(t, m)
}

// checkOneAttachment asserts the stream's single file attachment decoded.
func checkOneAttachment(t *testing.T, m *tnef.Message) {
	t.Helper()
	if len(m.Attachments) != 1 {
		t.Fatalf("attachments = %d, want 1", len(m.Attachments))
	}
	a := m.Attachments[0].Props
	if v := propValue(a, mapi.PrAttachLongFilename); v != "report.pdf" {
		t.Errorf("attachment name = %v", v)
	}
	if v, _ := propValue(a, mapi.PrAttachDataBin).([]byte); string(v) != "%PDF-1.4" {
		t.Errorf("attachment data = %q", v)
	}
}

// TestDecodeRefusesMalformedStreams proves a stream that does not conform is
// refused rather than half-read: a bad checksum, a trailing CRLF outside any
// attribute, a count larger than the stream, a wrong version and a foreign
// signature.
func TestDecodeRefusesMalformedStreams(t *testing.T) {
	good := tneftest.New().Attr(1, tneftest.AttSubject, tneftest.CString("s")).Bytes()
	hugeCount := tneftest.New().Attr(1, tneftest.AttMsgProps, []byte{0xFF, 0xFF, 0xFF, 0x7F}).Bytes()
	badVersion := []byte{0x78, 0x9F, 0x3E, 0x22, 0, 0}
	badVersion = append(badVersion, (&tneftest.Stream{}).RawAttr(1, tneftest.AttTnefVersion, []byte{0, 0, 2, 0}, 2).Bytes()...)
	for name, data := range map[string][]byte{
		"checksum":      tneftest.New().RawAttr(1, tneftest.AttSubject, tneftest.CString("s"), 0).Bytes(),
		"trailing CRLF": append(append([]byte{}, good...), '\r', '\n'),
		"huge count":    hugeCount,
		"version":       badVersion,
		"signature":     []byte("not a TNEF stream at all"),
	} {
		if _, err := tnef.Decode(data, nil); !errors.Is(err, tnef.ErrMalformed) {
			t.Errorf("%s: err = %v, want ErrMalformed", name, err)
		}
	}
	if _, err := tnef.Decode(good, nil); err != nil {
		t.Errorf("a conforming stream was refused: %v", err)
	}
}

// TestDecodeEmbeddedMessage proves an attached message's object value decodes as
// a stream of its own, and that nesting past the depth bound is refused.
func TestDecodeEmbeddedMessage(t *testing.T) {
	inner := tneftest.New().Attr(1, tneftest.AttSubject, tneftest.CString("inner")).Bytes()
	m, err := tnef.DecodeEmbedded(tnef.Object{IID: tnef.IIDMessage, Data: inner}, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	if v := propValue(m.Props, mapi.PrSubject); v != "inner" {
		t.Errorf("inner subject = %v", v)
	}
	if _, err := tnef.DecodeEmbedded(tnef.Object{IID: tnef.IIDMessage, Data: inner}, nil, 8); err == nil {
		t.Error("an attached message past the nesting bound was decoded")
	}
	if _, err := tnef.DecodeEmbedded(tnef.Object{Data: inner}, nil, 0); err == nil {
		t.Error("an object of another interface was decoded as a message")
	}
}
