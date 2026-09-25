package oxvcard

import (
	"strings"
	"testing"

	"hermex/internal/mapi"
	"hermex/internal/oxcmail"
)

// TestImportedPhotoIsTheContactPicture stores a vCard PHOTO as the flagged
// picture other surfaces read, and exports it back.
func TestImportedPhotoIsTheContactPicture(t *testing.T) {
	opt := Options{Resolver: newResolver().resolve}
	msg, err := Import([]byte(fullVCard), opt)
	mustNoErr(t, err, "import")
	att, ok := PhotoAttachment(msg)
	if !ok {
		t.Fatal("imported contact has no picture")
	}
	if v, _ := att.Props.Get(mapi.PrAttachmentContactPhoto); v != true {
		t.Error("the photo attachment is not flagged as the contact picture")
	}
	out, err := Export(msg, opt)
	mustNoErr(t, err, "export")
	if !strings.Contains(string(out), "PHOTO:data:image/png;base64,iVBORw0KGgo=") {
		t.Errorf("export lost the photo:\n%s", out)
	}
}

// TestPhotoAttachmentSkipsAFile picks the flagged picture over an earlier file,
// reads a nameless attachment as a picture stored before the flag, and never
// reads a named file as the picture.
func TestPhotoAttachmentSkipsAFile(t *testing.T) {
	file := oxcmail.Attachment{Props: mapi.PropertyValues{
		{Tag: mapi.PrAttachLongFilename, Value: "cv.pdf"}, {Tag: mapi.PrAttachDataBin, Value: []byte("pdf")}}}
	picture := oxcmail.Attachment{Props: mapi.PropertyValues{
		{Tag: mapi.PrAttachmentContactPhoto, Value: true}, {Tag: mapi.PrAttachDataBin, Value: []byte("jpg")}}}
	legacy := oxcmail.Attachment{Props: mapi.PropertyValues{{Tag: mapi.PrAttachDataBin, Value: []byte("old")}}}

	cases := []struct {
		name string
		atts []oxcmail.Attachment
		want string
	}{
		{"flagged", []oxcmail.Attachment{file, picture}, "jpg"},
		{"legacy", []oxcmail.Attachment{file, legacy}, "old"},
		{"file only", []oxcmail.Attachment{file}, ""},
	}
	for _, c := range cases {
		att, ok := PhotoAttachment(&oxcmail.Message{Attachments: c.atts})
		got := ""
		if ok {
			v, _ := att.Props.Get(mapi.PrAttachDataBin)
			got = string(v.([]byte))
		}
		if got != c.want {
			t.Errorf("%s: picture = %q, want %q", c.name, got, c.want)
		}
	}
}
