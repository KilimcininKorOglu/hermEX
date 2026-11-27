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

// TestEncodeWritesTheMSOXTNEFFraming checks the bytes against [MS-OXTNEF]
// 2.1.3.1, not against the decoder: the stream opens with the signature
// 0x223E9F78 and a 2-byte key, both little-endian, and the first attribute is
// attTnefVersion at the message level (1) whose checksum is the 16-bit sum of
// its data bytes.
func TestEncodeWritesTheMSOXTNEFFraming(t *testing.T) {
	raw, err := Encode(&Message{Props: []Prop{{Tag: mapi.PrSubject, Value: "s"}}})
	if err != nil {
		t.Fatal(err)
	}
	head := []byte{0x78, 0x9F, 0x3E, 0x22, 0, 0, 1, 0x06, 0x90, 0x08, 0x00, 4, 0, 0, 0, 0, 0, 1, 0, 1, 0}
	if len(raw) < len(head) || !reflect.DeepEqual(raw[:len(head)], head) {
		t.Errorf("stream head = % x, want % x", raw[:min(len(raw), len(head))], head)
	}
}

// TestEncodeRefusesAMistypedValue proves a value that cannot be written as its
// property type fails the stream rather than writing bytes a reader misreads: a
// string in a PtLong, a scalar in a multivalue type, and a named-range property
// (id 0x8000 and above, [MS-OXPROPS]) that carries no name to map it by.
func TestEncodeRefusesAMistypedValue(t *testing.T) {
	for name, p := range map[string]Prop{
		"string in a PtLong":      {Tag: mapi.PrImportance, Value: "high"},
		"scalar in a list":        {Tag: mapi.MakeTag(0x3FF0, mapi.PtMvUnicode), Value: "one"},
		"named id without a name": {Tag: mapi.MakeTag(0x8003, mapi.PtUnicode), Value: "x"},
	} {
		raw, err := Encode(&Message{Props: []Prop{p}})
		if err == nil {
			t.Errorf("%s: encoded", name)
		}
		if raw != nil {
			t.Errorf("%s: a refused message still returned %d bytes", name, len(raw))
		}
	}
}
