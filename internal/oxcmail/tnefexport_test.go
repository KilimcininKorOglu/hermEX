package oxcmail

import (
	"bytes"
	"strings"
	"testing"

	"hermex/internal/mapi"
	"hermex/internal/mime"
	"hermex/internal/tnef"
)

// tnefMessage is a message whose class MIME cannot carry, with one attachment.
func tnefMessage() *Message {
	return &Message{Props: mapi.PropertyValues{
		{Tag: mapi.PrMessageClass, Value: "IPM.Note.Custom"},
		{Tag: mapi.PrSubject, Value: "x"},
		{Tag: mapi.PrSenderSmtpAddress, Value: "alice@hermex.test"},
		{Tag: mapi.PrInternetMessageID, Value: "<tnef.1@hermex.test>"},
		{Tag: mapi.PrBody, Value: "body text"},
		{Tag: mapi.PrImportance, Value: int32(mapi.ImportanceHigh)},
	}, Attachments: []Attachment{{Props: mapi.PropertyValues{
		{Tag: mapi.PrAttachMethod, Value: int32(mapi.AttachByValue)},
		{Tag: mapi.PrAttachLongFilename, Value: "r.pdf"},
		{Tag: mapi.PrAttachDataBin, Value: []byte("%PDF")},
	}}}}
}

// TestTNEFExportCorrelatesWithTheMessageID proves the TNEF form names its stream in
// X-MS-TNEF-Correlator with the Message-ID exactly, and that the stream's
// PidTagTnefCorrelationKey holds the same bytes with no terminating null, so a
// reader that compares the two accepts the stream.
func TestTNEFExportCorrelatesWithTheMessageID(t *testing.T) {
	raw, err := Export(tnefMessage(), Options{TNEF: true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "X-MS-TNEF-Correlator: <tnef.1@hermex.test>\r\n") {
		t.Fatalf("no correlator header:\n%s", raw)
	}
	root := mime.ParseStructure(raw)
	part := findTNEF(root)
	if part == nil {
		t.Fatalf("no winmail.dat part:\n%s", raw)
	}
	data, err := part.DecodedContent()
	if err != nil {
		t.Fatal(err)
	}
	stream, err := tnef.Decode(data, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range stream.Props {
		if p.Tag == mapi.PrTnefCorrelationKey {
			if key, _ := p.Value.([]byte); !bytes.Equal(key, []byte("<tnef.1@hermex.test>")) {
				t.Errorf("correlation key = %q", key)
			}
			return
		}
	}
	t.Error("the stream carries no correlation key")
}

// TestTNEFExportRoundTrip proves a message sent in the TNEF form is read back with
// the class and the attachment the stream carries.
func TestTNEFExportRoundTrip(t *testing.T) {
	raw, err := Export(tnefMessage(), Options{TNEF: true})
	if err != nil {
		t.Fatal(err)
	}
	back, err := Import(raw, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if got := propString(back.Props, mapi.PrMessageClass); got != "IPM.Note.Custom" {
		t.Errorf("class = %q", got)
	}
	if got := propString(back.Props, mapi.PrBody); strings.TrimSpace(got) != "body text" {
		t.Errorf("body = %q", got)
	}
	if len(back.Attachments) != 1 || propString(back.Attachments[0].Props, mapi.PrAttachLongFilename) != "r.pdf" {
		t.Errorf("attachments = %+v", back.Attachments)
	}
}
