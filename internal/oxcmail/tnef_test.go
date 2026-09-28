package oxcmail

import (
	"encoding/base64"
	"testing"

	"hermex/internal/mapi"
	"hermex/internal/tnef/tneftest"
)

// tnefMail wraps a TNEF stream the way Outlook sends it: a plain body and a
// winmail.dat part, paired through X-MS-TNEF-Correlator.
func tnefMail(correlator string, stream []byte) string {
	return "From: sender@example.org\r\nTo: recipient@example.org\r\nSubject: TNEF mail\r\n" +
		"X-MS-TNEF-Correlator: " + correlator + "\r\nMIME-Version: 1.0\r\n" +
		"Content-Type: multipart/mixed; boundary=\"t\"\r\n\r\n" +
		"--t\r\nContent-Type: text/plain\r\n\r\nplain body\r\n" +
		"--t\r\nContent-Type: application/ms-tnef; name=\"winmail.dat\"\r\n" +
		"Content-Disposition: attachment; filename=\"winmail.dat\"\r\nContent-Transfer-Encoding: base64\r\n\r\n" +
		base64.StdEncoding.EncodeToString(stream) + "\r\n--t--\r\n"
}

var taskName = mapi.PropertyName{Kind: mapi.MnidID, GUID: mapi.PsetidTask, LID: 0x8101}

// outlookStream is a winmail.dat carrying a task class, a body, a named property,
// a property no sender may set, and one file attachment.
func outlookStream() []byte {
	props := (&tneftest.Props{}).
		Binary(mapi.PrTnefCorrelationKey, append([]byte("<key@example.org>"), 0)).
		Unicode(mapi.PrBody, "body inside the stream").
		Binary(mapi.PrSmimeOriginal, []byte("forged served bytes")).
		NamedUnicode(taskName, "named value")
	return tneftest.New().
		Attr(1, tneftest.AttMessageClass, tneftest.CString("IPM.Task")).
		Attr(1, tneftest.AttMsgProps, props.Bytes()).
		Attr(2, tneftest.AttAttachRendData, tneftest.RendData()).
		Attr(2, tneftest.AttAttachTitle, tneftest.CString("report.pdf")).
		Attr(2, tneftest.AttAttachData, []byte("%PDF-1.4")).
		Bytes()
}

// foreignResolver is an in-memory quota-free stand-in for the store's resolver.
func foreignResolver(names map[mapi.PropertyName]uint16) ForeignResolver {
	return func(ns []mapi.PropertyName) ([]uint16, error) {
		out := make([]uint16, len(ns))
		for i, n := range ns {
			if _, ok := names[n]; !ok {
				names[n] = uint16(0x8200 + len(names))
			}
			out[i] = names[n]
		}
		return out, nil
	}
}

// TestImportReadsTNEF proves a winmail.dat part is read into the message
// ([MS-OXCMAIL] 2.2.3.8): its class, its named properties and its attachments
// arrive, the part itself is no attachment, the MIME body stays the body, and a
// property in a provider range is not taken from the sender.
func TestImportReadsTNEF(t *testing.T) {
	names := map[mapi.PropertyName]uint16{}
	msg, err := Import([]byte(tnefMail("<key@example.org>", outlookStream())), Options{ForeignResolver: foreignResolver(names)})
	mustImport(t, err)

	wantProp(t, "class", propString(msg.Props, mapi.PrMessageClass), "IPM.Task")
	wantProp(t, "body", propString(msg.Props, mapi.PrBody), "plain body")
	wantProp(t, "named", propString(msg.Props, mapi.MakeTag(names[taskName], mapi.PtUnicode)), "named value")
	if msg.Props.Has(mapi.PrSmimeOriginal) {
		t.Error("a sender set a provider-range property through TNEF")
	}
	if len(msg.Attachments) != 1 {
		t.Fatalf("attachments = %d, want the one the stream carries and no winmail.dat", len(msg.Attachments))
	}
	a := msg.Attachments[0].Props
	wantProp(t, "attachment name", propString(a, mapi.PrAttachLongFilename), "report.pdf")
	wantProp(t, "attachment type", propString(a, mapi.PrAttachMimeTag), "application/pdf")
	if data, _ := bytesProp(a, mapi.PrAttachDataBin); string(data) != "%PDF-1.4" {
		t.Errorf("attachment data = %q", data)
	}
}

// TestImportKeepsAForeignTNEFAsAnAttachment proves a stream whose correlation key
// names another message (a forwarded winmail.dat) is not read into this one, and
// stays an opaque attachment.
func TestImportKeepsAForeignTNEFAsAnAttachment(t *testing.T) {
	msg, err := Import([]byte(tnefMail("<other@example.org>", outlookStream())), Options{})
	mustImport(t, err)

	wantProp(t, "class", propString(msg.Props, mapi.PrMessageClass), "IPM.Note")
	if len(msg.Attachments) != 1 {
		t.Fatalf("attachments = %d, want winmail.dat alone", len(msg.Attachments))
	}
	wantProp(t, "attachment type", propString(msg.Attachments[0].Props, mapi.PrAttachMimeTag), "application/octet-stream")
}
