package oxcmail

import (
	"bytes"
	"mime/quotedprintable"

	"hermex/internal/mapi"
)

// quotedPrintable encodes data as quoted-printable with CRLF line breaks.
func quotedPrintable(data []byte) []byte {
	var buf bytes.Buffer
	w := quotedprintable.NewWriter(&buf)
	// A bytes.Buffer never fails a write, so neither does the encoder over it.
	_, _ = w.Write(data)
	_ = w.Close() // flushes into the same buffer, which cannot fail
	return buf.Bytes()
}

// vCardType is the media type of a vCard 3.0 attachment ([MS-OXCMAIL] 2.1.3.4.6).
const vCardType = `text/directory; profile="vCard"; charset=utf-8`

// withContactCards returns the message as it is sent: a copy whose attached
// contacts are replaced by their vCards.
func withContactCards(msg *Message, opt Options) (*Message, error) {
	atts, err := contactCards(msg.Attachments, opt)
	if err != nil {
		return nil, err
	}
	sent := *msg
	sent.Attachments = atts
	return &sent, nil
}

// contactCards replaces each attached contact with the vCard [MS-OXCMAIL]
// 2.1.3.4.6 sends in its place. The message's own attachment list is left alone,
// so the stored message keeps its embedded contact.
func contactCards(atts []Attachment, opt Options) ([]Attachment, error) {
	if opt.ContactCard == nil {
		return atts, nil
	}
	out := make([]Attachment, len(atts))
	for i, att := range atts {
		blob, ok := bytesProp(att.Props, mapi.PrEmbeddedContact)
		if !ok || len(blob) == 0 {
			out[i] = att
			continue
		}
		card, name, err := opt.ContactCard(blob)
		if err != nil {
			return nil, err
		}
		var a Attachment
		a.Props.Set(mapi.PrAttachMethod, int32(mapi.AttachByValue))
		a.Props.Set(mapi.PrAttachMimeTag, vCardType)
		a.Props.Set(mapi.PrAttachLongFilename, name+".vcf")
		a.Props.Set(mapi.PrAttachDataBin, card)
		out[i] = a
	}
	return out, nil
}
