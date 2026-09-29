package tnef

import (
	"reflect"
	"testing"

	"hermex/internal/mapi"
)

// TestEncodeRoundTrip proves a stream Encode writes decodes back to the same
// properties and attachments, named properties included, and that an 8-bit
// string arrives as the text it held.
func TestEncodeRoundTrip(t *testing.T) {
	name := mapi.PropertyName{GUID: IIDMessage, Kind: mapi.MnidString, Name: "Keyword"}
	lid := mapi.PropertyName{GUID: IIDMessage, Kind: mapi.MnidID, LID: 0x8501}
	in := &Message{
		Props: []Prop{
			{Tag: mapi.PrMessageClass, Value: "IPM.Note.Custom"},
			{Tag: mapi.PrSubject, Value: "Şubat raporu"},
			{Tag: mapi.PrImportance, Value: int32(2)},
			{Tag: mapi.PrClientSubmitTime, Value: uint64(133000000000000000)},
			{Tag: mapi.PrTnefCorrelationKey, Value: []byte("<id@hermex.test>")},
			{Tag: mapi.MakeTag(0x8000, mapi.PtUnicode), Name: &name, Value: "odak"},
			{Tag: mapi.MakeTag(0x8001, mapi.PtBoolean), Name: &lid, Value: true},
			{Tag: mapi.MakeTag(0x8002, mapi.PtMvUnicode), Name: &name, Value: []string{"a", "bc"}},
			{Tag: mapi.MakeTag(0x3FFA, mapi.PtString8), Value: "latin"},
		},
		Attachments: []Attachment{{Props: []Prop{
			{Tag: mapi.PrAttachLongFilename, Value: "r.pdf"},
			{Tag: mapi.PrAttachDataBin, Value: []byte("%PDF-1")},
		}}},
	}
	raw, err := Encode(in)
	if err != nil {
		t.Fatal(err)
	}
	out, err := Decode(raw, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := append([]Prop{{Tag: mapi.PrMessageClass, Value: "IPM.Note.Custom"}}, in.Props[:8]...)
	want = append(want, Prop{Tag: mapi.MakeTag(0x3FFA, mapi.PtUnicode), Value: "latin"})
	if !reflect.DeepEqual(out.Props, want) {
		t.Errorf("props:\n got %+v\nwant %+v", out.Props, want)
	}
	if len(out.Attachments) != 1 {
		t.Fatalf("attachments = %d", len(out.Attachments))
	}
	got := out.Attachments[0].Props
	if len(got) < 2 || !reflect.DeepEqual(got[len(got)-2:], in.Attachments[0].Props) {
		t.Errorf("attachment props = %+v", got)
	}
}

// TestEncodeRefusesAMistypedValue proves a value whose Go type does not match its
// property type fails the stream rather than writing bytes a reader misreads.
func TestEncodeRefusesAMistypedValue(t *testing.T) {
	if _, err := Encode(&Message{Props: []Prop{{Tag: mapi.PrImportance, Value: "high"}}}); err == nil {
		t.Fatal("a string in a PtLong property was encoded")
	}
}
