package oxcmail

import (
	"bytes"
	"mime/multipart"
	"net/textproto"
	"strings"

	"hermex/internal/mapi"
	"hermex/internal/tnef"
)

// A recipient whose PidTagSendRichInfo is true reads the message's properties from
// a TNEF part ([MS-OXCMAIL] 2.1.3.1.1): the mail is a multipart/mixed of the plain
// text body and an application/ms-tnef winmail.dat that carries every property and
// attachment. X-MS-TNEF-Correlator names the stream's PidTagTnefCorrelationKey, the
// Message-ID, so a reader can tell the stream belongs to this message.

// SendsRichInfo reports whether a recipient's PidTagSendRichInfo asks for the TNEF
// form.
func SendsRichInfo(bag mapi.PropertyValues) bool {
	v, _ := bag.Get(mapi.PrSendRichInfo)
	b, _ := v.(bool)
	return b
}

// sendsTNEF reports whether Export writes the TNEF form. A meeting message keeps
// its iCalendar form, because that is the part a scheduling client acts on.
func sendsTNEF(msg *Message, opt Options) bool {
	return opt.TNEF && len(opt.CalendarBody) == 0 &&
		!strings.HasPrefix(propString(msg.Props, mapi.PrMessageClass), "IPM.Schedule.Meeting.")
}

// writeTNEFBody writes the TNEF form of a message after its header block.
func writeTNEFBody(b *bytes.Buffer, msg *Message, opt Options) error {
	key := propString(msg.Props, mapi.PrInternetMessageID)
	stream, err := tnefStream(msg, opt, key)
	if err != nil {
		return err
	}
	if key != "" {
		// The key is written exactly as the Message-ID, with no terminating null: a
		// reader compares it byte for byte with the stream's key.
		writeField(b, "X-MS-TNEF-Correlator", key)
	}
	var parts bytes.Buffer
	mw := multipart.NewWriter(&parts)
	if err := writeAlternativePart(mw, "text/plain; charset=utf-8", []byte(propString(msg.Props, mapi.PrBody))); err != nil {
		return err
	}
	h := textproto.MIMEHeader{}
	h.Set("Content-Type", "application/ms-tnef; name=\"winmail.dat\"")
	h.Set("Content-Transfer-Encoding", "base64")
	h.Set("Content-Disposition", "attachment; filename=\"winmail.dat\"")
	pw, err := mw.CreatePart(h)
	if err != nil {
		return err
	}
	if _, err := pw.Write(encodeBase64(stream)); err != nil {
		return err
	}
	if err := mw.Close(); err != nil {
		return err
	}
	writeField(b, "Content-Type", "multipart/mixed; boundary=\""+mw.Boundary()+"\"")
	b.WriteString("\r\n")
	b.Write(parts.Bytes())
	return nil
}

// tnefStream encodes the message's properties and attachments as a TNEF stream.
func tnefStream(msg *Message, opt Options, key string) ([]byte, error) {
	props, err := tnefProps(msg.Props, opt)
	if err != nil {
		return nil, err
	}
	if key != "" {
		props = append(props, tnef.Prop{Tag: mapi.PrTnefCorrelationKey, Value: []byte(key)})
	}
	t := &tnef.Message{Props: props}
	for _, a := range msg.Attachments {
		ap, err := tnefProps(tnefAttachment(a.Props), opt)
		if err != nil {
			return nil, err
		}
		t.Attachments = append(t.Attachments, tnef.Attachment{Props: ap})
	}
	return tnef.Encode(t)
}

// tnefAttachment returns an attachment's properties as a stream carries them. An
// embedded message is stored as its RFC 5322 bytes, not as a message object, so it
// travels as a message/rfc822 file.
func tnefAttachment(props mapi.PropertyValues) mapi.PropertyValues {
	if method, _ := propInt32(props, mapi.PrAttachMethod); method != mapi.AttachEmbeddedMsg {
		return props
	}
	out := append(mapi.PropertyValues(nil), props...)
	out.Set(mapi.PrAttachMethod, int32(mapi.AttachByValue))
	out.Set(mapi.PrAttachMimeTag, "message/rfc822")
	return out
}

// tnefProps selects the properties a stream carries: the ones a sender may set,
// with each named property under its name. A named property the store cannot name
// is left out, as a reader could not map it.
func tnefProps(bag mapi.PropertyValues, opt Options) ([]tnef.Prop, error) {
	var out []tnef.Prop
	for _, p := range bag {
		if p.Tag.ID() < 0x8000 {
			if transmittable(p.Tag) && p.Tag != mapi.PrTnefCorrelationKey {
				out = append(out, tnef.Prop{Tag: p.Tag, Value: p.Value})
			}
			continue
		}
		if opt.PropName == nil {
			continue
		}
		name, ok, err := opt.PropName(p.Tag.ID())
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, tnef.Prop{Tag: p.Tag, Name: &name, Value: p.Value})
		}
	}
	return out, nil
}
